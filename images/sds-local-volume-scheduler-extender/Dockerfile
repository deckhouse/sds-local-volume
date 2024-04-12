ARG BASE_GOLANG_22_ALPINE=registry.deckhouse.io/base_images/golang:1.22.1-alpine@sha256:0de6cf7cceab6ecbf0718bdfb675b08b78113c3709c5e4b99456cdb2ae8c2495
ARG BASE_GOLANG_22_ALPINE_BUILDER=$BASE_GOLANG_22_ALPINE

FROM $BASE_GOLANG_22_ALPINE_BUILDER as builder

WORKDIR /go/src

ADD go.mod .
ADD go.sum .

RUN go mod download

COPY . .

WORKDIR /go/src/cmd
RUN GOOS=linux GOARCH=amd64 go build -o sds-local-volume-scheduler-extender

FROM --platform=linux/amd64 $BASE_GOLANG_22_ALPINE
COPY --from=builder /go/src/cmd/sds-local-volume-scheduler-extender /go/src/cmd/sds-local-volume-scheduler-extender
RUN apk add curl

ENTRYPOINT ["/go/src/cmd/sds-local-volume-scheduler-extender"]
