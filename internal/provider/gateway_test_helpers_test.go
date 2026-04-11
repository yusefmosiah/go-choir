package provider

// newGatewayClientForTest is not available in this package due to import
// cycle restrictions (gateway → provider). The GatewayCaller mock in
// gateway_bridge_test.go provides sufficient coverage. HTTP-level
// integration between GatewayClient and GatewayBridgeProvider is tested
// in the gateway package's tests.
