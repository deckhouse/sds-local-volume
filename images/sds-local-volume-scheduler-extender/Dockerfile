ARG BASE_GOLANG_22_ALPINE=registry.deckhouse.io/base_images/golang:1.22.3-alpine@sha256:dbf216b880b802c22e3f4f2ef0a78396b4a9a6983cb9b767c5efc351ebf946b0
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
