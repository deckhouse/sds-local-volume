ARG BASE_ALPINE=registry.deckhouse.io/base_images/alpine:3.16.3@sha256:5548e9172c24a1b0ca9afdd2bf534e265c94b12b36b3e0c0302f5853eaf00abb
ARG BASE_GOLANG_ALPINE_BUILDER=registry.deckhouse.io/base_images/golang:1.22.6-alpine@sha256:41ad3e6d91b2e10a15e871672578b11a51c7f2eb126c82e193e38b647da0bbb2

FROM $BASE_GOLANG_ALPINE_BUILDER as builder

WORKDIR /go/src

ADD go.mod .
ADD go.sum .

RUN go mod download

COPY . .

WORKDIR /go/src/cmd
RUN GOOS=linux GOARCH=amd64 go build -o sds-local-volume-csi

FROM --platform=linux/amd64 $BASE_ALPINE

ENV DEBIAN_FRONTEND noninteractive
RUN apk add --no-cache lvm2 e2fsprogs e2fsprogs-extra xfsprogs xfsprogs-extra blkid

COPY --from=builder /go/src/cmd/sds-local-volume-csi /go/src/cmd/sds-local-volume-csi

ENTRYPOINT ["/go/src/cmd/sds-local-volume-csi"]
