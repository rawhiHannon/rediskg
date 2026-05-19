# Redis 8 + FalkorDB module on Ubuntu 24.04.
#
# Why Ubuntu 24.04 and not the redis:8 / falkordb images:
#   - falkordb.so requires GLIBC_2.39, GLIBCXX_3.4.32, libssl.so.3 (OpenSSL 3).
#   - redis:8 image is Debian bookworm => glibc 2.36 (too old).
#   - falkordb/falkordb image is Redis 7.x => fails rediskg's "Redis >= 8" check.
# Ubuntu 24.04 ships glibc 2.39 + OpenSSL 3, and Redis 8 comes from the
# official Redis apt repo.
FROM ubuntu:24.04

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      curl gpg ca-certificates \
      libssl3 libgomp1 libstdc++6 \
 && curl -fsSL https://packages.redis.io/gpg \
      | gpg --dearmor -o /usr/share/keyrings/redis-archive-keyring.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/redis-archive-keyring.gpg] https://packages.redis.io/deb noble main" \
      > /etc/apt/sources.list.d/redis.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends redis \
 && rm -rf /var/lib/apt/lists/*

# Bake the module into the image. Replace with a newer build if you ever
# need to bump past the FalkorDB version in this repo.
COPY falkordb.so /opt/falkordb.so

EXPOSE 6379

# Load the module at startup so rediskg's setup sees it already present
# (skips the MODULE LOAD / download / build path entirely).
# protected-mode no + bind 0.0.0.0 so the host-side rediskg can connect.
CMD ["redis-server", \
     "--bind", "0.0.0.0", \
     "--protected-mode", "no", \
     "--loadmodule", "/opt/falkordb.so"]
