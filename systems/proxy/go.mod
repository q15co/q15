module github.com/q15co/q15/systems/proxy

go 1.25.5

require (
	github.com/elazarl/goproxy v1.8.2
	github.com/q15co/q15/libs/proxy-contract v0.0.0
	go.yaml.in/yaml/v3 v3.0.4
	google.golang.org/grpc v1.81.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/q15co/q15/libs/proxy-contract => ../../libs/proxy-contract
