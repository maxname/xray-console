# A throwaway "VPS" for exercising the installer: Debian with systemd and sshd,
# root login by password. Never use this image for anything real.
ARG BASE=debian:12
FROM ${BASE}

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus openssh-server sudo ca-certificates iproute2 procps \
 && rm -rf /var/lib/apt/lists/* \
 && echo 'root:xc-test-root' | chpasswd \
 && sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config \
 && systemctl enable ssh \
 && systemctl mask getty.target console-getty.service systemd-logind.service \
      systemd-udevd.service systemd-udev-trigger.service

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
