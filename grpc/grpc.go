// Package grpc is the gRPC Transport Starter, and it owns no server.
//
// connect-go's generated constructor returns (string, http.Handler), which is
// exactly the shape of web.Server.Handle, so a connect service mounts on the
// HTTP Starter's listener with no adapter and no second port. Since Go 1.24
// one cleartext port serves HTTP/1 and HTTP/2 together, so that one listener
// answers gRPC, gRPC-Web, Connect JSON and plain REST at once. See ADR 0006.
//
// There is no grpc.addr and no Config at all. It is the first Starter with
// none, and the missing key is the first thing a reader looks for: the
// address belongs to goboot/web, and two ports is web.New called twice.
//
// What is left in this package is the handler options that make connect safe
// by default. They are not the whole of it: the sanitiser passes a
// *connect.Error through untouched on purpose, so an adapter that wraps a raw
// error in one hands the caller that error's text however carefully these
// options are wired. See docs/spec.md 4.0.
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/squall-chua/go-boot"
)

// DefaultOptions is a slice you can edit, not hidden behaviour, the same
// shape as web.DefaultMiddleware. Print it, drop an entry, or splice one in:
//
//	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))
//
// The three entries are panic recovery, the error sanitiser, and connect's
// required protocol header.
//
// There is no logging or request-ID interceptor, and that is not an omission.
// Under the shared listener web.DefaultMiddleware has already run, so
// goboot.LoggerFrom(ctx) and the request ID reach a connect handler free.
//
// One honest asymmetry with web.Use: connect options are per SERVICE, not per
// server, so these are repeated at every mount. connect has no global
// registry and there is no way around it.
func DefaultOptions(log *slog.Logger) []connect.HandlerOption {
	return []connect.HandlerOption{
		// Recovery returns a clean CodeInternal rather than letting the panic
		// reach net/http, which would reset the stream and tell the caller
		// nothing. It is first so it wraps everything after it.
		connect.WithRecover(func(ctx context.Context, spec connect.Spec, _ http.Header, p any) error {
			logFrom(ctx, log).Error("panic in rpc", "procedure", spec.Procedure, "panic", p)
			return connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}),
		connect.WithInterceptors(sanitiseErrors(log)),
		// Without this, a plain browser fetch can reach an RPC as a simple
		// cross-origin POST. It applies to the Connect protocol only, so gRPC
		// and gRPC-Web clients are unaffected.
		connect.WithRequireConnectProtocolHeader(),
	}
}

// sanitiseErrors is mandatory, not a nicety. Measured in #12: a bare error
// returned from a connect handler reaches the caller VERBATIM — the string
// `pq: password authentication failed for user "app" at 10.0.0.5:5432` went
// out on the wire, host and username and all.
//
// So anything that is not already a *connect.Error is replaced with a bare
// CodeUnknown, and the real error is logged with the procedure instead. An
// error the handler built with connect.NewError is passed through untouched:
// the handler chose what to say, so it is allowed to say it.
//
// It stays unexported: docs/spec.md 4.4 lists DefaultOptions as the whole of
// this package's API, and a user who drops the entry is replacing the rule,
// not re-adding it.
func sanitiseErrors(log *slog.Logger) connect.Interceptor { return sanitiser{log: log} }

type sanitiser struct{ log *slog.Logger }

func (s sanitiser) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		if err == nil {
			return res, nil
		}
		// The response is dropped along with the error. connect would ignore
		// it anyway, and returning both invites a caller to read a response
		// that was never sent.
		return nil, s.clean(ctx, req.Spec().Procedure, err)
	}
}

// WrapStreamingHandler is here because streaming is supported, so a streaming
// handler can leak exactly the same string a unary one can.
func (s sanitiser) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return s.clean(ctx, conn.Spec().Procedure, next(ctx, conn))
	}
}

// WrapStreamingClient does nothing. This interceptor sanitises what a server
// sends, and a client is the party being protected from, not protected.
func (s sanitiser) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// clean is the whole rule, in one place, so unary and streaming cannot drift.
//
// The log line is not decoration. The HTTP access log records 200 for a
// failed gRPC or gRPC-Web call, because the status rides in trailers, so this
// is the only place the failure is written down.
func (s sanitiser) clean(ctx context.Context, procedure string, err error) error {
	if err == nil {
		return nil
	}
	log := logFrom(ctx, s.log)
	var ce *connect.Error
	if errors.As(err, &ce) {
		log.Error("rpc failed", "procedure", procedure, "code", ce.Code().String(), "err", err)
		return err
	}
	log.Error("rpc failed", "procedure", procedure, "err", err)
	return connect.NewError(connect.CodeUnknown, errors.New("unknown"))
}

// logFrom prefers the request-scoped logger web.DefaultMiddleware attached,
// because it carries the requestId that joins this line to the access line
// for the same request — and the access line says 200, so this is the only
// line that says what went wrong.
//
// goboot.LoggerFrom falls back to slog.Default() when no middleware ran, and
// slog.Default() is not the App's logger, so the logger DefaultOptions was
// given is used instead in that case.
func logFrom(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if log := goboot.LoggerFrom(ctx); log != slog.Default() {
		return log
	}
	return fallback
}
