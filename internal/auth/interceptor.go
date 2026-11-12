package auth

import (
	"context"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ctxKey struct{}

func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

type Policy map[string][]string

const servicePrefix = "/inventory.v1.InventoryService/"

func DefaultPolicy() Policy {
	both := []string{RoleReader, RoleLibrarian}
	staff := []string{RoleLibrarian}
	return Policy{
		servicePrefix + "GetCopy":            both,
		servicePrefix + "WatchCopyStatus":    both,
		servicePrefix + "RegisterCopy":       staff,
		servicePrefix + "UpdateCopyStatus":   staff,
		servicePrefix + "BulkRegisterCopies": staff,
		servicePrefix + "SyncInventory":      staff,
	}
}

type Authenticator struct {
	secret []byte
	policy Policy
	now    func() time.Time
}

func NewAuthenticator(secret []byte, policy Policy) *Authenticator {
	return &Authenticator{secret: secret, policy: policy, now: time.Now}
}

func (a *Authenticator) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := a.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func (a *Authenticator) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := a.authorize(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

func (a *Authenticator) authorize(ctx context.Context, fullMethod string) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}

	raw := values[0]
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = raw[7:]
	}

	claims, err := Verify(a.secret, raw, a.now())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	if !strings.HasPrefix(fullMethod, "/grpc.reflection.") {
		allowed, known := a.policy[fullMethod]
		if !known {
			return nil, status.Errorf(codes.PermissionDenied, "no access policy for %s", fullMethod)
		}
		if !slices.Contains(allowed, claims.Role) {
			return nil, status.Errorf(codes.PermissionDenied,
				"role %q is not allowed to call %s", claims.Role, fullMethod)
		}
	}

	return context.WithValue(ctx, ctxKey{}, claims), nil
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

type perRPCToken struct {
	token      string
	requireTLS bool
}

func NewPerRPCToken(token string, requireTLS bool) credentials.PerRPCCredentials {
	return perRPCToken{token: token, requireTLS: requireTLS}
}

func (t perRPCToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

func (t perRPCToken) RequireTransportSecurity() bool { return t.requireTLS }
