// gomuks/push - An FCM push gateway for gomuks android.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"go.mau.fi/util/exerrors"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/util/exzerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/util/requestlog"
	"go.mau.fi/zeroconfig"
	"google.golang.org/api/option"
)

var fcmPackageName = os.Getenv("FCM_PACKAGE_NAME")
var fcmClient *messaging.Client

func init() {
	if _, hasPort := os.LookupEnv("PORT"); !hasPort {
		exerrors.PanicIfNotNil(os.Setenv("PORT", "8080"))
	}
}

func main() {
	log := exerrors.Must((&zeroconfig.Config{
		Writers: []zeroconfig.WriterConfig{{
			Type:     zeroconfig.WriterTypeStdout,
			Format:   zeroconfig.LogFormatPrettyColored,
			MinLevel: ptr.Ptr(zerolog.InfoLevel),
		}, {
			Type:   zeroconfig.WriterTypeFile,
			Format: zeroconfig.LogFormatJSON,
			FileConfig: zeroconfig.FileConfig{
				Filename:   "/var/log/gomuks-push.log",
				MaxSize:    100 * 1024,
				MaxAge:     7,
				MaxBackups: 10,
			},
		}},
		MinLevel: ptr.Ptr(zerolog.TraceLevel),
	}).Compile())
	exzerolog.SetupDefaults(log)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_gomuks/push/fcm", handlePushProxy)
	mux.HandleFunc("GET /{$}", handleIndex)
	server := http.Server{
		Addr: fmt.Sprintf("%s:%s", os.Getenv("HOST"), os.Getenv("PORT")),
		Handler: exhttp.ApplyMiddleware(
			mux,
			hlog.NewHandler(*log),
			requestlog.AccessLogger(requestlog.Options{TrustXForwardedFor: true}),
		),
	}
	ctx := log.WithContext(context.Background())
	app := exerrors.Must(firebase.NewApp(ctx, nil, option.WithCredentialsFile(os.Getenv("FCM_CREDENTIALS_FILE"))))
	fcmClient = exerrors.Must(app.Messaging(ctx))
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		exerrors.PanicIfNotNil(server.Shutdown(ctx))
		cancel()
	}()
	log.Info().Str("listen_address", server.Addr).Msg("Starting server")
	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

//go:embed index.html
var indexPage []byte

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexPage)
}

type PushRequest struct {
	Token        string `json:"token"`
	Owner        string `json:"owner"`
	Payload      []byte `json:"payload"`
	HighPriority bool   `json:"high_priority"`
}

func (pr *PushRequest) ToFCM() *messaging.Message {
	return &messaging.Message{
		Data: map[string]string{
			"payload": base64.StdEncoding.EncodeToString(pr.Payload),
		},
		Android: &messaging.AndroidConfig{
			RestrictedPackageName: fcmPackageName,
			Priority:              pr.GetPriority(),
		},
		Token: pr.Token,
	}
}

func (pr *PushRequest) GetPriority() string {
	if pr.HighPriority {
		return "high"
	}
	return "normal"
}

const maxPayloadLength = 4000
const maxContentLength = 4096

func handlePushProxy(w http.ResponseWriter, r *http.Request) {
	var req PushRequest
	if r.URL.Path != "/_gomuks/push/fcm" {
		w.WriteHeader(http.StatusNotFound)
	} else if r.ContentLength > maxContentLength {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
	} else if base64.StdEncoding.EncodedLen(len(req.Payload)) > maxPayloadLength {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	} else if resp, err := fcmClient.Send(r.Context(), req.ToFCM()); err != nil {
		hlog.FromRequest(r).
			Err(err).
			Str("push_token", req.Token).
			Str("owner", req.Owner).
			Msg("Failed to send FCM request")
		// TODO can errors be checked properly?
		if err.Error() == "Requested entity was not found." || err.Error() == "SenderId mismatch" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	} else {
		hlog.FromRequest(r).
			Err(err).
			Str("push_token", req.Token).
			Str("message_id", resp).
			Str("owner", req.Owner).
			Msg("Sent FCM request")
		w.WriteHeader(http.StatusOK)
	}
}
