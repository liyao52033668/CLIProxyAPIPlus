#!/bin/sh
# www.codebuddy.ai's authoritative DNS hands the blackhole address 0.0.0.1 to
# resolvers located outside China (Railway's included), so every request to the
# CodeBuddy AI upstream hangs until the client timeout. The edge server itself
# accepts overseas connections, so pin the last verified edge IP to bypass the
# poisoned resolution. If CodeBuddy AI starts timing out again after an upstream
# CDN rotation, re-resolve www.codebuddy.ai from a China-side resolver and
# update the address below.
if ! grep -q 'www\.codebuddy\.ai' /etc/hosts 2>/dev/null; then
    echo '43.170.214.92 www.codebuddy.ai' >> /etc/hosts \
        || echo 'entrypoint: failed to pin www.codebuddy.ai in /etc/hosts' >&2
fi

exec "$@"
