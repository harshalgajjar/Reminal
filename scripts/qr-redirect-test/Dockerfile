# reminal plus a real QR decoder (zbar), to check that every QR reminal writes
# (to a file, a pipe or a terminal) scans back to the link printed beside it.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates procps bsdutils python3 python3-pil python3-pip libzbar0 \
 && pip3 install --break-system-packages -q pyzbar pyte && rm -rf /var/lib/apt/lists/*
COPY reminal /usr/local/bin/reminal
COPY decode.py check.sh /t/
ENV REMINAL_RELAY=ws://127.0.0.1:18080/ws REMINAL_WEB=http://127.0.0.1:18080
CMD ["sh", "/t/check.sh"]
