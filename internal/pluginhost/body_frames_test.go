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
