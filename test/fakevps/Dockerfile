FROM ubuntu:24.04

ENV container=docker
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        dbus \
        dbus-user-session \
        git \
        iproute2 \
        openssh-client \
        procps \
        python3 \
        sudo \
        systemd \
        systemd-sysv \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash -g operator operator && \
    echo 'operator ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/operator && \
    chmod 0440 /etc/sudoers.d/operator

COPY test/fakevps/fakes/npm /usr/local/bin/npm
COPY test/fakevps/fakes/claude /usr/local/bin/claude
COPY test/fakevps/fakes/codex /usr/local/bin/codex
COPY test/fakevps/fakes/installer-curl /usr/local/bin/installer-curl
COPY test/fakevps/fakes/vendor-curl /usr/local/bin/vendor-curl

RUN chmod 0755 /usr/local/bin/npm /usr/local/bin/claude /usr/local/bin/codex \
    /usr/local/bin/installer-curl /usr/local/bin/vendor-curl

# The product Claude head units, from the same files the codvps binary
# embeds (internal/systemd/units), so scenarios run the exact
# claude-remote@.service and claude-remote.slice that ship. /etc/systemd/user
# is searched by every user's --user manager. python3 above is only for the
# fakes (fakes/claude's workspace-trust check, fakes/codex's daemon), not
# for codvps.
COPY internal/systemd/units/claude-remote@.service /etc/systemd/user/claude-remote@.service
COPY internal/systemd/units/claude-remote.slice /etc/systemd/user/claude-remote.slice

# The product Codex head units, from the same embedded files: the system
# codex-remote@.service template (run as the operator, no sandbox of its
# own) and the --user watchdog timer/service.
COPY internal/systemd/units/codex-remote@.service /etc/systemd/system/codex-remote@.service
COPY internal/systemd/units/codex-remote-watchdog.service /etc/systemd/user/codex-remote-watchdog.service
COPY internal/systemd/units/codex-remote-watchdog.timer /etc/systemd/user/codex-remote-watchdog.timer

# The product Cursor head unit, from the same embedded files: the --user
# cursor-remote@.service template.
COPY internal/systemd/units/cursor-remote@.service /etc/systemd/user/cursor-remote@.service

RUN systemctl mask \
        console-getty.service \
        dev-hugepages.mount \
        dev-mqueue.mount \
        getty.target \
        sys-fs-fuse-connections.mount \
        sys-kernel-config.mount \
        sys-kernel-debug.mount \
        systemd-remount-fs.service \
        systemd-tmpfiles-setup-dev.service \
        systemd-tmpfiles-setup.service \
        systemd-udev-trigger.service \
        systemd-udevd.service

STOPSIGNAL SIGRTMIN+3

CMD ["/sbin/init"]
