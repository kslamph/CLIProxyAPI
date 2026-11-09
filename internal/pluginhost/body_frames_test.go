package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCallPluginWithBodyFrameOmitsBodyFields(t *testing.T) {
	client := &framedRequestClient{}
	req := rpcExecutorRequest{HostCallbackID: "cb", ExecutorRequest: executorRequestFixture()}
	_, errCall := callPluginWithBodyFrame[rpcEmptyResponse](context.Background(), client, "executor.execute", req, pluginabi.BodyFrameExecutor, [][]byte{[]byte("original"), []byte("payload")}, true)
	if errCall != nil {
		t.Fatalf("callPluginWithBodyFrame() error = %v", errCall)
	}
	frame, errDecode := pluginabi.DecodeBodyFrame(client.request)
	if errDecode != nil {
		t.Fatalf("DecodeBodyFrame() error = %v", errDecode)
	}
	if len(frame.Segments) != 2 || !bytes.Equal(frame.Segments[0], []byte("original")) || !bytes.Equal(frame.Segments[1], []byte("payload")) {
		t.Fatalf("segments = %#v", frame.Segments)
	}
	var metadata rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(frame.Metadata, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(metadata.OriginalRequest) != 0 || len(metadata.Payload) != 0 {
		t.Fatal("framed metadata retained body fields")
	}
}

type framedRequestClient struct{ request []byte }

func (c *framedRequestClient) Call(_ context.Context, _ string, request []byte) ([]byte, error) {
	c.request = bytes.Clone(request)
	return marshalRPCResult(rpcEmptyResponse{})
}
func (c *framedRequestClient) Shutdown() {}

func executorRequestFixture() pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{OriginalRequest: []byte("original"), Payload: []byte("payload")}
}

func TestCallPluginWithBodyFrameFallsBackToLegacyJSONWhenFrameTooLarge(t *testing.T) {
	original := encodeBodyFrame
	encodeBodyFrame = func(pluginabi.BodyFrameKind, []byte, ...[]byte) ([]byte, error) {
		return nil, pluginabi.ErrBodyFrameTooLarge
	}
	t.Cleanup(func() { encodeBodyFrame = original })

	cases := []struct {
		name    string
		request any
		kind    pluginabi.BodyFrameKind
		check   func(*testing.T, []byte)
		bodies  [][]byte
		wantKey string
	}{
		{
			name:    "executor",
			request: rpcExecutorRequest{HostCallbackID: "cb-exec", ExecutorRequest: executorRequestFixture()},
			kind:    pluginabi.BodyFrameExecutor,
			bodies:  [][]byte{[]byte("original"), []byte("payload")},
			check: func(t *testing.T, raw []byte) {
				var got rpcExecutorRequest
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.HostCallbackID != "cb-exec" || string(got.OriginalRequest) != "original" || string(got.Payload) != "payload" {
					t.Fatalf("legacy request = %#v", got)
				}
			},
		},
		{
			name:    "http",
			request: rpcExecutorHTTPRequest{HostCallbackID: "cb-http", ExecutorHTTPRequest: pluginapi.ExecutorHTTPRequest{Body: []byte("body")}},
			kind:    pluginabi.BodyFrameHostHTTP,
			bodies:  [][]byte{[]byte("body")},
			check: func(t *testing.T, raw []byte) {
				var got rpcExecutorHTTPRequest
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.HostCallbackID != "cb-http" || string(got.Body) != "body" {
					t.Fatalf("legacy request = %#v", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &framedRequestClient{}
			if _, err := callPluginWithBodyFrame[rpcEmptyResponse](context.Background(), client, "m", tc.request, tc.kind, tc.bodies, true); err != nil {
				t.Fatalf("callPluginWithBodyFrame() error = %v", err)
			}
			if pluginabiHasFrameMagic(client.request) {
				t.Fatal("oversize request was framed")
			}
			tc.check(t, client.request)
		})
	}
}

func TestCallPluginWithBodyFramePropagatesOtherEncodeErrors(t *testing.T) {
	original := encodeBodyFrame
	encodeBodyFrame = func(pluginabi.BodyFrameKind, []byte, ...[]byte) ([]byte, error) {
		return nil, pluginabi.ErrInvalidBodyFrame
	}
	t.Cleanup(func() { encodeBodyFrame = original })
	client := &framedRequestClient{}
	req := rpcExecutorRequest{ExecutorRequest: executorRequestFixture()}
	if _, err := callPluginWithBodyFrame[rpcEmptyResponse](context.Background(), client, "m", req, pluginabi.BodyFrameExecutor, [][]byte{nil, nil}, true); err == nil {
		t.Fatal("expected invalid frame error to propagate")
	}
	if client.request != nil {
		t.Fatal("request was sent despite encode failure")
	}
}

func pluginabiHasFrameMagic(raw []byte) bool { return bytes.HasPrefix(raw, []byte("CPBF")) }

func TestDecodeHostHTTPCallRequestFramedMatchesLegacy(t *testing.T) {
	body := bytes.Repeat([]byte{0, 1, 2, 0xff}, 1024)
	profile := &pluginapi.HTTPWireProfile{HeaderProfile: []string{"a"}}
	flat := rpcHostHTTPRequest{HostCallbackID: "cb", OperationID: " op ", Method: "POST", URL: "http://x", Headers: httpHeader{"K": {"v"}}, WireProfile: profile}
	nested := rpcHostHTTPRequest{HostCallbackID: "cb", OperationID: "op", Request: &httpRequest{Method: "POST", URL: "http://x", Headers: httpHeader{"K": {"v"}}}, WireProfile: profile}
	instance := &hostCallbackInstance{}
	instance.binaryBodyFrames.Store(true)
	framedCtx := withHostCallbackIdentity(context.Background(), "p", instance)

	for name, meta := range map[string]rpcHostHTTPRequest{"flat": flat, "nested": nested} {
		t.Run(name, func(t *testing.T) {
			metadata, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := pluginabi.EncodeBodyFrame(pluginabi.BodyFrameHostHTTP, metadata, body)
			if err != nil {
				t.Fatal(err)
			}
			got, callbackID, operationID, err := decodeHostHTTPCallRequest(framedCtx, frame)
			if err != nil {
				t.Fatal(err)
			}
			legacyMeta := meta
			if legacyMeta.Request != nil {
				r := *legacyMeta.Request
				r.Body = body
				legacyMeta.Request = &r
			} else {
				legacyMeta.Body = body
			}
			legacyRaw, _ := json.Marshal(legacyMeta)
			want, wantCallback, wantOp, err := decodeHostHTTPCallRequest(context.Background(), legacyRaw)
			if err != nil {
				t.Fatal(err)
			}
			if callbackID != "cb" || callbackID != wantCallback || operationID != "op" || operationID != wantOp {
				t.Fatalf("ids = %q/%q, want %q/%q", callbackID, operationID, wantCallback, wantOp)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if !bytes.Equal(gotJSON, wantJSON) || !bytes.Equal(got.Body, body) {
				t.Fatalf("framed decode differs from legacy:\n got %s\nwant %s", gotJSON[:200], wantJSON[:200])
			}
			frame[len(frame)-1] ^= 0xff // mutate source frame: decoded body must be an independent copy
			if !bytes.Equal(got.Body, body) {
				t.Fatal("decoded body aliases the frame buffer")
			}
		})
	}
}

func TestDecodeHostHTTPCallRequestFramedRejectsInvalid(t *testing.T) {
	instance := &hostCallbackInstance{}
	instance.binaryBodyFrames.Store(true)
	ctx := withHostCallbackIdentity(context.Background(), "p", instance)
	if _, _, _, err := decodeHostHTTPCallRequest(ctx, []byte(`{"method":"GET"}`)); err == nil {
		t.Fatal("legacy JSON accepted on a framed instance")
	}
	exec, _ := pluginabi.EncodeBodyFrame(pluginabi.BodyFrameExecutor, []byte(`{}`), []byte("a"))
	if _, _, _, err := decodeHostHTTPCallRequest(ctx, exec); err == nil {
		t.Fatal("wrong frame kind accepted")
	}
	two, _ := pluginabi.EncodeBodyFrame(pluginabi.BodyFrameHostHTTP, []byte(`{}`), []byte("a"), []byte("b"))
	if _, _, _, err := decodeHostHTTPCallRequest(ctx, two); err == nil {
		t.Fatal("two-segment host HTTP frame accepted")
	}
	bad, _ := pluginabi.EncodeBodyFrame(pluginabi.BodyFrameHostHTTP, []byte(`[]`), []byte("a"))
	if _, _, _, err := decodeHostHTTPCallRequest(ctx, bad); err == nil {
		t.Fatal("non-object metadata accepted")
	}
}
