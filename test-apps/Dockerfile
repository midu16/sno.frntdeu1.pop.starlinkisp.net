# Use Fedora as the base image
FROM fedora:42

LABEL maintainer="midu@redhat.com"

# Install bind and utilities
RUN dnf install -y bind bind-utils && \
    dnf clean all

# Create directories for configs and zones
RUN mkdir -p /etc/bind/zones /var/cache/bind && \
    chown -R named:named /etc/bind /var/cache/bind

# Expose DNS ports
EXPOSE 53/tcp 53/udp

# Use 'named' user for running the daemon
USER named

# Command to run bind in foreground with custom config
CMD ["named", "-g", "-c", "/etc/bind/named.conf"]
