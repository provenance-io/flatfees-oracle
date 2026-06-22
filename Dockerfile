# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25 AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# Static build so it runs on a distroless/scratch base.
RUN CGO_ENABLED=0 GOFLAGS=-trimpath go build -ldflags="-s -w" -o /out/oracle ./cmd/oracle

# ---- runtime stage ----
FROM gcr.io/distroless/static:nonroot
# Timezone data is embedded in the binary via the time/tzdata import, so the
# runtime image needs nothing beyond the static binary and CA certs (present in
# distroless/static).
COPY --from=build /out/oracle /usr/local/bin/oracle
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/oracle"]
