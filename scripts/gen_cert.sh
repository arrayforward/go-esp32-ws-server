#!/bin/bash
# 生成端侧友好的自签证书：ECDSA P-256（非 RSA！）
#
# 为什么不用 RSA：ESP32-S3 (Xtensa LX7) 上 RSA-2048 握手签名验证约需数百毫秒
# 且耗电高；ECDSA P-256 快约 10 倍。AES-256-GCM 套件由 S3 硬件 AES 加速。
#
# 产物：
#   server_ca.pem —— 证书（网关 -tls-cert，同时拷到设备 certs/ 作 CA）
#   server.key    —— 私钥（网关 -tls-key，勿外泄）
#
# 用法：./scripts/gen_cert.sh [CN，默认 router.local]
set -e
CN=${1:-router.local}
OUT=$(dirname "$0")/..
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -days 3650 -keyout "$OUT/server.key" -out "$OUT/server_ca.pem" \
  -subj "/CN=$CN" \
  -addext "subjectAltName=DNS:$CN,DNS:localhost,IP:127.0.0.1"
echo "generated: server_ca.pem server.key (ECDSA P-256, CN=$CN)"
echo "设备端：cp server_ca.pem ~/goldie_esp32/components/convai_ws/certs/"
