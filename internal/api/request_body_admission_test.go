package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
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
		{name: "empty", body: 0, want: 3},
		{name: "under quantum", body: 1, want: 3},
		{name: "exact quantum", body: 3, want: 3},
		{name: "rounds up", body: 4, want: 6},
		{name: "caps at budget", body: 99, want: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := a.cost(test.body, 0); got != test.want {
				t.Fatalf("cost(%d) = %d, want %d", test.body, got, test.want)
			}
		})
	}
	if got := a.cost(-1, 0); got != 8 {
		t.Fatalf("unknown length cost = %d, want full budget 8", got)
	}
	if got := a.cost(-1, 4); got != 6 {
		t.Fatalf("unknown length with max body cost = %d, want 6", got)
	}
}

func TestRequestBodyAdmissionCancellationReleasesWaiter(t *testing.T) {
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: 4, QuantumBytes: 2})
	ctx := context.Background()
	release, ok := a.acquire(ctx, 4, 0)
	if !ok {
		t.Fatal("initial acquire failed")
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	acquired := make(chan bool, 1)
	go func() {
		releaseWaiter, acquiredOK := a.acquire(waitCtx, 4, 0)
		if acquiredOK {
			releaseWaiter.release()
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
	release.release()
	releaseNext, ok := a.acquire(context.Background(), 4, 0)
	if !ok {
		t.Fatal("admission capacity leaked after cancellation")
	}
	releaseNext.release()
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

// admissibleNow reports whether a body would be admitted immediately, without
// holding a reservation or waiting. A canceled context makes acquire return
// instead of blocking when there is no capacity.
func admissibleNow(a *requestBodyAdmission, contentLength int64) bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, ok := a.acquire(ctx, contentLength, 0)
	r.release()
	return ok
}

func usedBytes(a *requestBodyAdmission) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.used
}

func TestRequestBodyAdmissionQuantumReloadKeepsByteAccounting(t *testing.T) {
	const mib = int64(1 << 20)
	enabled := func(budget, quantum int64) proxyconfig.RequestBodyAdmissionConfig {
		return proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: budget, QuantumBytes: quantum}
	}

	t.Run("smaller quantum does not admit extra bytes", func(t *testing.T) {
		a := newRequestBodyAdmission(enabled(128*mib, 2*mib))
		var held []*requestBodyReservation
		for i := 0; i < 64; i++ {
			r, ok := a.acquire(context.Background(), 2*mib, 0)
			if !ok {
				t.Fatalf("acquire %d failed", i)
			}
			held = append(held, r)
		}
		a.configure(enabled(128*mib, mib))
		if got := usedBytes(a); got != 128*mib {
			t.Fatalf("used = %d after reload, want %d", got, 128*mib)
		}
		if admissibleNow(a, mib) {
			t.Fatal("body admitted beyond byte budget after quantum was reduced")
		}
		for _, r := range held {
			r.release()
		}
		if got := usedBytes(a); got != 0 {
			t.Fatalf("used = %d after release, want 0", got)
		}
	})

	t.Run("larger quantum does not block free capacity", func(t *testing.T) {
		a := newRequestBodyAdmission(enabled(128*mib, mib))
		held := make([]*requestBodyReservation, 0, 64)
		for i := 0; i < 64; i++ {
			r, ok := a.acquire(context.Background(), mib, 0)
			if !ok {
				t.Fatalf("acquire %d failed", i)
			}
			held = append(held, r)
		}
		a.configure(enabled(128*mib, 2*mib))
		// 64MiB is held, so exactly 64MiB (32 quanta of 2MiB) is still free.
		for i := 0; i < 32; i++ {
			r, ok := func() (*requestBodyReservation, bool) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return a.acquire(ctx, 2*mib, 0)
			}()
			if !ok {
				t.Fatalf("free capacity refused at step %d after quantum was increased", i)
			}
			held = append(held, r)
		}
		if admissibleNow(a, 2*mib) {
			t.Fatal("body admitted beyond budget")
		}
		for _, r := range held {
			r.release()
		}
		if got := usedBytes(a); got != 0 {
			t.Fatalf("used = %d after release, want 0", got)
		}
	})

	t.Run("waiter across reload uses the current quantum and releases cleanly", func(t *testing.T) {
		a := newRequestBodyAdmission(enabled(8*mib, 4*mib))
		first, ok := a.acquire(context.Background(), 4*mib, 0)
		if !ok {
			t.Fatal("first acquire failed")
		}
		second, ok := a.acquire(context.Background(), 4*mib, 0)
		if !ok {
			t.Fatal("second acquire failed")
		}
		type result struct {
			r  *requestBodyReservation
			ok bool
		}
		done := make(chan result, 1)
		go func() {
			r, okAcquire := a.acquire(context.Background(), 3*mib, 0)
			done <- result{r, okAcquire}
		}()
		// Wake the waiter through a reload while it is parked, then free capacity.
		a.configure(enabled(8*mib, mib))
		first.release()
		got := <-done
		if !got.ok {
			t.Fatal("waiter was not admitted after capacity was released")
		}
		if want := 4*mib + 3*mib; usedBytes(a) != want {
			t.Fatalf("used = %d, want %d (4MiB held + 3MiB waiter at the reloaded 1MiB quantum)", usedBytes(a), want)
		}
		got.r.release()
		second.release()
		if got := usedBytes(a); got != 0 {
			t.Fatalf("used = %d after release, want 0", got)
		}
	})

	t.Run("cancel and double release across reload do not leak or underflow", func(t *testing.T) {
		a := newRequestBodyAdmission(enabled(4*mib, 2*mib))
		held, ok := a.acquire(context.Background(), 4*mib, 0)
		if !ok {
			t.Fatal("acquire failed")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan bool, 1)
		go func() {
			r, okAcquire := a.acquire(ctx, 2*mib, 0)
			r.release()
			done <- okAcquire
		}()
		a.configure(enabled(4*mib, mib))
		cancel()
		if <-done {
			t.Fatal("canceled waiter was admitted")
		}
		held.release()
		held.release()
		if got := usedBytes(a); got != 0 {
			t.Fatalf("used = %d, want 0", got)
		}
	})
}

func TestRequestBodyReservationShrinkIsOneShotAndNeverBlocks(t *testing.T) {
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: 8, QuantumBytes: 2})
	r, ok := a.acquire(context.Background(), -1, 0)
	if !ok {
		t.Fatal("acquire failed")
	}
	if got := usedBytes(a); got != 8 {
		t.Fatalf("used = %d, want 8", got)
	}
	r.Shrink(3)
	if got := usedBytes(a); got != 4 {
		t.Fatalf("used after Shrink(3) = %d, want 4", got)
	}
	r.Shrink(1)
	if got := usedBytes(a); got != 4 {
		t.Fatalf("second Shrink changed used to %d, want 4", got)
	}
	r.release()
	r.Shrink(1)
	if got := usedBytes(a); got != 0 {
		t.Fatalf("used = %d after release, want 0", got)
	}
}

func TestRequestBodyAdmissionReservesDecodedLimitUntilBodyIsRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const mib = int64(1 << 20)
	const maxBody = 8 * mib
	a := newRequestBodyAdmission(proxyconfig.RequestBodyAdmissionConfig{Enabled: true, BudgetBytes: 16 * mib, QuantumBytes: 2 * mib})

	var enc bytes.Buffer
	zw, errWriter := zstd.NewWriter(&enc)
	if errWriter != nil {
		t.Fatalf("zstd.NewWriter() error = %v", errWriter)
	}
	decoded := bytes.Repeat([]byte("a"), int(5*mib))
	if _, errWrite := zw.Write(decoded); errWrite != nil {
		t.Fatalf("zstd write error = %v", errWrite)
	}
	if errClose := zw.Close(); errClose != nil {
		t.Fatalf("zstd close error = %v", errClose)
	}
	if int64(enc.Len()) >= 2*mib {
		t.Fatalf("test payload compressed to %d bytes, want under one quantum", enc.Len())
	}

	var usedBefore, usedAfter int64
	var gotLen int
	engine := gin.New()
	engine.Use(requestBodyAdmissionMiddleware(a, func(*http.Request) int64 { return maxBody }))
	engine.Use(func(c *gin.Context) {
		c.Set(handlers.MaxRequestBodyBytesContextKey, maxBody)
		c.Next()
	})
	engine.POST("/", func(c *gin.Context) {
		usedBefore = usedBytes(a)
		body, errRead := handlers.ReadRequestBody(c)
		if errRead != nil {
			t.Errorf("ReadRequestBody() error = %v", errRead)
		}
		gotLen = len(body)
		usedAfter = usedBytes(a)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(enc.Bytes()))
	req.Header.Set("Content-Encoding", "zstd")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if gotLen != len(decoded) {
		t.Fatalf("decoded length = %d, want %d", gotLen, len(decoded))
	}
	if usedBefore != maxBody {
		t.Fatalf("reservation before decode = %d, want decoded limit %d (not the compressed length)", usedBefore, maxBody)
	}
	if want := 6 * mib; usedAfter != want {
		t.Fatalf("reservation after decode = %d, want %d (decoded body rounded to quantum)", usedAfter, want)
	}
	if got := usedBytes(a); got != 0 {
		t.Fatalf("used = %d after request, want 0", got)
	}
}
