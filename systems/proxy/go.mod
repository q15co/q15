module github.com/q15co/q15/systems/proxy

go 1.25.5

require (
	github.com/elazarl/goproxy v1.8.2
	github.com/q15co/q15/libs/proxy-contract v0.0.0
	go.yaml.in/yaml/v3 v3.0.5
	google.golang.org/grpc v1.81.1
)

require (
	github.com/stretchr/testify v1.12.1 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/q15co/q15/libs/proxy-contract => ../../libs/proxy-contract
