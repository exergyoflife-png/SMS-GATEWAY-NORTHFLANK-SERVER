//nolint:testpackage // Tests exercise the internal config-loading error boundary directly.
package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	infrahttp "github.com/capcom6/go-infra-fx/http"
	"go.uber.org/fx"
)

func TestDefaultRequiresExplicitGatewayMode(t *testing.T) {
	t.Parallel()

	if got := Default().Gateway.Mode; got != "" {
		t.Fatalf("expected no default gateway mode, got %q", got)
	}
}

func TestValidateConfigGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       GatewayMode
		privateKey string
		wantErr    string
	}{
		{
			name:    "missing mode",
			wantErr: "gateway mode must be explicitly configured",
		},
		{
			name:    "unsupported mode",
			mode:    GatewayMode("internal"),
			wantErr: "unsupported gateway mode",
		},
		{
			name:    "mode is not normalized implicitly",
			mode:    GatewayMode(" public "),
			wantErr: "unsupported gateway mode",
		},
		{
			name: "public mode",
			mode: GatewayModePublic,
		},
		{
			name:    "private mode without token",
			mode:    GatewayModePrivate,
			wantErr: "gateway private token must not be blank",
		},
		{
			name:       "private mode with whitespace token",
			mode:       GatewayModePrivate,
			privateKey: " \t\r\n ",
			wantErr:    "gateway private token must not be blank",
		},
		{
			name:       "private mode with token",
			mode:       GatewayModePrivate,
			privateKey: "registration-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Default()
			cfg.Gateway.Mode = tt.mode
			cfg.Gateway.PrivateToken = tt.privateKey

			err := validateConfig(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected config to be valid, got %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err)
			}
		})
	}
}

func TestFinishConfigLoadPropagatesLoaderError(t *testing.T) {
	t.Parallel()

	loadErr := errors.New("malformed configuration")
	_, err := finishConfigLoad(Default(), loadErr)
	if !errors.Is(err, loadErr) {
		t.Fatalf("expected loader error to be preserved, got %v", err)
	}
}

func TestFinishConfigLoadValidatesLoadedConfig(t *testing.T) {
	t.Parallel()

	_, err := finishConfigLoad(Default(), nil)
	if err == nil {
		t.Fatal("expected missing gateway mode to fail validation")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("expected validation error context, got %q", err)
	}
}

func TestConfigModuleStartupBoundary(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		mode       GatewayMode
		token      string
		wantErr    string
	}{
		{
			name:       "configuration load error",
			configYAML: "gateway: [",
			mode:       GatewayModePrivate,
			token:      "registration-secret",
			wantErr:    "failed to load config",
		},
		{
			name:    "missing mode",
			wantErr: "gateway mode must be explicitly configured",
		},
		{
			name:    "invalid mode",
			mode:    "internal",
			wantErr: "unsupported gateway mode",
		},
		{
			name:    "private mode without token",
			mode:    GatewayModePrivate,
			wantErr: "gateway private token must not be blank",
		},
		{
			name: "explicit public mode",
			mode: GatewayModePublic,
		},
		{
			name:  "valid private mode",
			mode:  GatewayModePrivate,
			token: "registration-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(configPath, []byte(tt.configYAML), 0o600); err != nil {
				t.Fatalf("write config fixture: %v", err)
			}
			t.Setenv("CONFIG_PATH", configPath)
			t.Setenv("GATEWAY__MODE", string(tt.mode))
			t.Setenv("GATEWAY__PRIVATE_TOKEN", tt.token)

			app := fx.New(
				Module(),
				fx.Invoke(func(infrahttp.Config) {}),
				fx.NopLogger,
			)
			err := app.Err()
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				err = app.Start(ctx)
				cancel()
			}

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected startup error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected startup error containing %q, got %q", tt.wantErr, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("expected startup to succeed, got %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := app.Stop(ctx); err != nil {
				t.Fatalf("stop app: %v", err)
			}
		})
	}
}
