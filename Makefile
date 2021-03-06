PROTO_DIR=./api/proto
OUTPUT_DIR=./api
gen-all: gen-client-proto gen-raft-proto gen-cluster-services-proto gen-internal-request-proto
	echo "generating"

gen-client-proto:
	protoc -I=${PROTO_DIR} -I=${GOPATH}/src -I=${GOPATH}/src/github.com/gogo/protobuf/protobuf --gofast_out=plugins=grpc:${OUTPUT_DIR}/kv-client/ ${PROTO_DIR}/kv_client_api.proto
	protoc -I=${PROTO_DIR} -I=${GOPATH}/src -I=${GOPATH}/src/github.com/gogo/protobuf/protobuf --gofast_out=plugins=grpc:${OUTPUT_DIR}/kv-client/ ${PROTO_DIR}/healthcheck.proto

gen-raft-proto:
	protoc -I=./pkg/raft/proto -I=${GOPATH}/src -I=${GOPATH}/src/github.com/gogo/protobuf/protobuf --gofast_out=plugins=grpc:./pkg/raft/proto pkg/raft/proto/raft.proto

gen-cluster-services-proto:
	protoc -I=${PROTO_DIR} -I=${GOPATH}/src -I=${GOPATH}/src/github.com/gogo/protobuf/protobuf --gofast_out=plugins=grpc:${OUTPUT_DIR}/cluster-services/ ${PROTO_DIR}/cluster_services.proto

gen-internal-request-proto:
	protoc -I=${PROTO_DIR} -I=${GOPATH}/src -I=${GOPATH}/src/github.com/gogo/protobuf/protobuf --gofast_out=plugins=grpc:${OUTPUT_DIR}/internal_request/ ${PROTO_DIR}/internal_request.proto