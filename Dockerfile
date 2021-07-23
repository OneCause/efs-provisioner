# Copyright 2017 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM golang:1.16.6-alpine3.14 AS builder
ENV CGO_ENABLED=0
COPY . /go/src/github.com/OneCause/efs-provisioner
WORKDIR /go/src/github.com/OneCause/efs-provisioner
RUN go build -o /go/bin/efs-provisioner ./main.go

FROM alpine:3.14.0
RUN apk add --no-cache ca-certificates
COPY --from=builder /go/bin/efs-provisioner /
ENTRYPOINT ["/efs-provisioner"]