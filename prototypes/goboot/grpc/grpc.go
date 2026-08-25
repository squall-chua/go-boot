// Package grpc is a THROWAWAY stub of the go-boot gRPC Transport Starter as
// settled in #12. It owns NO server, no Component and no config: connect-go's
// generated constructor returns (string, http.Handler), which is exactly
// web.Server.Handle. What is left is handler options.
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
)

// DefaultOptions is a slice you can edit, the same shape as
// web.DefaultMiddleware. No logging interceptor: web's middleware already ran,
// so goboot.LoggerFrom(ctx) reaches the handler free.
func DefaultOptions(log *slog.Logger) []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithRecover(func(_ context.Context, _ connect.Spec, _ http.Header, v any) error {
			log.Error("panic in rpc", "err", v)
			return connect.NewError(connect.CodeInternal, errors.New("internal"))
		}),
		connect.WithInterceptors(sanitiseErrors(log)),
		connect.WithRequireConnectProtocolHeader(),
	}
}

// sanitiseErrors replaces any non-*connect.Error with a bare CodeUnknown and
// logs the real one. Measured in #12: a bare error reaches the caller
// verbatim, password and all.
func sanitiseErrors(log *slog.Logger) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			res, err := next(ctx, req)
			if err == nil {
				return res, nil
			}
			var ce *connect.Error
			if errors.As(err, &ce) {
				log.Error("rpc failed", "procedure", req.Spec().Procedure, "code", ce.Code())
				return res, err
			}
			// The access log records 200 for a failed gRPC call, so this line
			// is where the truth lives.
			log.Error("rpc failed", "procedure", req.Spec().Procedure, "err", err)
			return nil, connect.NewError(connect.CodeUnknown, errors.New("unknown"))
		}
	})
}
