package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRequestBodyAdmissionConfigDefaultsAndValidation(t *testing.T) {
	cfg, errParse := proxyconfig.ParseConfigBytes([]byte("config-version: 8\nserver: {}\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.RequestBodyAdmission.Enabled {
		t.Fatal("request body admission should be enabled by default")
	}
	if cfg.RequestBodyAdmission.BudgetBytes != 128<<20 {
		t.Fatalf("budget = %d, want %d", cfg.RequestBodyAdmission.BudgetBytes, 128<<20)
	}
	if cfg.RequestBodyAdmission.QuantumBytes != 2<<20 {
		t.Fatalf("quantum = %d, want %d", cfg.RequestBodyAdmission.QuantumBytes, 2<<20)
	}

	for _, raw := range []string{
		"config-version: 8\nserver:\n  request-body-admission:\n    budget-bytes: 0\n",
		"config-version: 8\nserver:\n  request-body-admission:\n    quantum-bytes: -1\n",
		"config-version: 8\nserver:\n  request-body-admission:\n    budget-bytes: 9223372036854775807\n    quantum-bytes: 2\n",
	} {
		if _, errInvalid := proxyconfig.ParseConfigBytes([]byte(raw)); errInvalid == nil {
			t.Fatalf("ParseConfigBytes(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestRequestBodyAdmissionWeightRoundingAndUnknownLength(t *testing.T) {
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{
		Enabled:      true,
		BudgetBytes:  8,
		QuantumBytes: 3,
	})
	for _, test := range []struct {
		name string
		body int64
		want int64
	}{
		{name: "empty", body: 0, want: 1},
		{name: "under quantum", body: 1, want: 1},
		{name: "exact quantum", body: 3, want: 1},
		{name: "rounds up", body: 4, want: 2},
		{name: "caps at budget", body: 99, want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := a.weight(test.body); got != test.want {
				t.Fatalf("weight(%d) = %d, want %d", test.body, got, test.want)
			}
		})
	}
	if got := a.weight(-1); got != 3 {
		t.Fatalf("unknown length weight = %d, want full budget weight 3", got)
	}
	if got := a.weightWithMax(-1, 4); got != 2 {
		t.Fatalf("unknown length with max body weight = %d, want 2", got)
	}
}

func TestRequestBodyAdmissionCancellationReleasesWaiter(t *testing.T) {
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: 4, QuantumBytes: 2})
	ctx := context.Background()
	release, ok := a.acquire(ctx, 2)
	if !ok {
		t.Fatal("initial acquire failed")
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	acquired := make(chan bool, 1)
	go func() {
		releaseWaiter, acquiredOK := a.acquire(waitCtx, 2)
		if acquiredOK {
			releaseWaiter()
		}
		acquired <- acquiredOK
	}()
	cancel()
	select {
	case got := <-acquired:
		if got {
			t.Fatal("canceled waiter acquired admission")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	release()
	releaseNext, ok := a.acquire(context.Background(), 4)
	if !ok {
		t.Fatal("admission capacity leaked after cancellation")
	}
	releaseNext()
}

func TestRequestBodyAdmissionRunsBeforeBodyMaterialization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: 4, QuantumBytes: 2})
	engine := gin.New()
	engine.Use(requestBodyAdmissionMiddleware(a, func(*http.Request) int64 { return 4 }))
	engine.POST("/", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
		c.Status(http.StatusNoContent)
	})
	counter := &countingBodyReader{reader: strings.NewReader("payload")}
	req := httptest.NewRequest(http.MethodPost, "/", counter)
	req.ContentLength = -1
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if got := counter.read.Load(); got == 0 {
		t.Fatal("handler did not receive admitted body")
	}
}
