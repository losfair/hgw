FROM debian:bullseye-slim

RUN apt-get update
RUN DEBIAN_FRONTEND=noninteractive apt-get install --no-install-recommends -y \
  dropbear ca-certificates udhcpc net-tools iproute2 iputils-ping procps \
  htop chrony curl tmux file wireguard-tools cryptsetup-bin psmisc minicom \
  tcpdump strace gpiod
RUN apt-get remove -y systemd && apt-get autoremove -y && apt-get clean
RUN rm -rf /etc/dropbear && rm -rf /var/lib/apt/lists/* && rm /bin/su
RUN useradd -s /bin/bash -d /tmp/user user

COPY ./init ./init2 /
COPY ./homegw-init ./homegw-rt /
