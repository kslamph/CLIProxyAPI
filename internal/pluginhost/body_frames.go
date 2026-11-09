package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func callPluginWithBodyFrame[T any](ctx context.Context, client pluginClient, method string, request any, kind pluginabi.BodyFrameKind, segments [][]byte, enabled bool) (T, error) {
	if !enabled {
		return callPlugin[T](ctx, client, method, request)
	}
	metadataRequest := sanitizePluginRequest(request)
	switch req := metadataRequest.(type) {
	case rpcExecutorRequest:
		req.OriginalRequest = nil
		req.Payload = nil
		metadataRequest = req
	case rpcExecutorHTTPRequest:
		req.Body = nil
		metadataRequest = req
	default:
		return callPlugin[T](ctx, client, method, request)
	}
	metadata, errMarshal := json.Marshal(metadataRequest)
	if errMarshal != nil {
		var zero T
		return zero, fmt.Errorf("marshal framed plugin request %s: %w", method, errMarshal)
	}
	rawRequest, errFrame := pluginabi.EncodeBodyFrame(kind, metadata, segments...)
	if errFrame != nil {
		var zero T
		return zero, fmt.Errorf("encode framed plugin request %s: %w", method, errFrame)
	}
	return callPluginRaw[T](ctx, client, method, rawRequest)
}

// decodeHostHTTPCallRequest decodes a plugin->host HTTP callback request. When
// the calling plugin negotiated binary body frames the raw body segment is used
// directly, so the body is never re-encoded as Base64 JSON.
func decodeHostHTTPCallRequest(ctx context.Context, raw []byte) (pluginapi.HTTPRequest, string, string, error) {
	if !hostCallbackBinaryBodyFrames(ctx) {
		return decodeHostHTTPRequestWithOperationID(raw)
	}
	frame, errDecode := pluginabi.DecodeBodyFrame(raw)
	if errDecode != nil {
		return pluginapi.HTTPRequest{}, "", "", errDecode
	}
	if frame.Kind != pluginabi.BodyFrameHostHTTP || len(frame.Segments) != 1 {
		return pluginapi.HTTPRequest{}, "", "", fmt.Errorf("invalid host HTTP body frame: expected one host HTTP segment")
	}
	var req rpcHostHTTPRequest
	if errUnmarshal := json.Unmarshal(frame.Metadata, &req); errUnmarshal != nil {
		return pluginapi.HTTPRequest{}, "", "", fmt.Errorf("decode framed host HTTP metadata: %w", errUnmarshal)
	}
	if req.Request != nil {
		req.Request.Body = frame.Segments[0]
	} else {
		req.Body = frame.Segments[0]
	}
	httpReq, callbackID, operationID := hostHTTPRequestFromRPC(req)
	return httpReq, callbackID, operationID, nil
}
