# Vula OS — Container
#
# Build: docker build -t vulos .
# Run:   docker run -p 8080:8080 --shm-size=1g vulos
# Open:  http://localhost:8080

FROM node:22-alpine AS frontend
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html vite.config.js eslint.config.js ./
COPY src/ src/
COPY public/ public/
RUN npm run build

FROM golang:alpine AS backend
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY backend/ .
RUN go mod download && go build -ldflags="-s -w" -o /vulos-server ./cmd/server

FROM alpine:edge

# Core packages + remote browser stack (Xvfb + Chromium + GStreamer)
RUN apk add --no-cache \
    tini bash sudo shadow python3 curl jq ca-certificates \
    xvfb-run chromium xdotool \
    gstreamer-tools gst-plugins-base gst-plugins-good gst-plugins-bad \
    pulseaudio pulseaudio-utils \
    font-noto socat \
    && curl -fsSL https://curl.se/ca/cacert.pem -o /etc/ssl/certs/ca-certificates.crt

# Sudo infrastructure — users are created dynamically at registration
RUN addgroup -S wheel 2>/dev/null || true \
    && echo "%wheel ALL=(ALL) ALL" > /etc/sudoers.d/wheel \
    && chmod 440 /etc/sudoers.d/wheel

RUN mkdir -p /opt/vulos/webroot /opt/vulos/apps \
    /var/lib/vulos /root/.vulos/data /root/.vulos/db /root/.vulos/sandbox \
    /tmp/xdg-runtime

COPY --from=backend /vulos-server /usr/local/bin/vulos-server
COPY --from=frontend /app/dist /opt/vulos/webroot
COPY apps/ /opt/vulos/apps/
COPY landing/ /opt/vulos/landing/
COPY registry.json /opt/vulos/registry.json

RUN touch /var/lib/vulos/.setup-complete

ENV PORT=8080
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV XDG_RUNTIME_DIR=/tmp/xdg-runtime
ENV WLR_BACKENDS=headless
ENV WLR_RENDERER=pixman
ENV VULOS_REGISTRY=/opt/vulos/registry.json
ENV SHELL=/bin/bash
ENV DISPLAY=:99
ENV HOSTNAME=vula

EXPOSE 8080 3000
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/usr/local/bin/vulos-server", "-env", "local"]
