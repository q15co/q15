module github.com/q15co/q15/systems/exec-service

go 1.25.5

require (
	github.com/q15co/q15/libs/exec-contract v0.0.0
	github.com/q15co/q15/libs/proxy-contract v0.0.0
	google.golang.org/grpc v1.72.2
	google.golang.org/protobuf v1.36.9
)

require (
	golang.org/x/net v0.35.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250218202821-56aae31c358a // indirect
)

replace github.com/q15co/q15/libs/exec-contract => ../../libs/exec-contract

replace github.com/q15co/q15/libs/proxy-contract => ../../libs/proxy-contract
