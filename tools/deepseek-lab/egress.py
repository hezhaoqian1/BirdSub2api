"""Hosted requests connect only to validated public IPs, including DNS answers.

Pin the checked address at socket creation so DNS cannot change between checking
and connecting. HTTPS keeps the original hostname for SNI/certificate validation.
"""
import ipaddress
import socket

from requests.adapters import HTTPAdapter
from urllib3.connection import HTTPConnection, HTTPSConnection
from urllib3.connectionpool import HTTPConnectionPool, HTTPSConnectionPool
from urllib3.util.connection import create_connection


def public_socket(conn):
    records = socket.getaddrinfo(conn.host, conn.port, type=socket.SOCK_STREAM)
    addresses = list(dict.fromkeys(r[4][0] for r in records))
    if not addresses or any(not ipaddress.ip_address(ip).is_global for ip in addresses):
        raise ValueError('hosted_private_address_blocked')
    error = None
    for ip in addresses:
        try:
            return create_connection((ip, conn.port), conn.timeout, conn.source_address, conn.socket_options)
        except OSError as exc:
            error = exc
    raise error


class PublicHTTPConnection(HTTPConnection):
    def _new_conn(self):return public_socket(self)


class PublicHTTPSConnection(HTTPSConnection):
    def _new_conn(self):return public_socket(self)


class PublicHTTPPool(HTTPConnectionPool):
    ConnectionCls = PublicHTTPConnection


class PublicHTTPSPool(HTTPSConnectionPool):
    ConnectionCls = PublicHTTPSConnection


class PublicOnlyAdapter(HTTPAdapter):
    def init_poolmanager(self, *args, **kwargs):
        super().init_poolmanager(*args, **kwargs)
        self.poolmanager.pool_classes_by_scheme = {'http': PublicHTTPPool, 'https': PublicHTTPSPool}
