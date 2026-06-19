"""Put the collector package dir on sys.path so tests can import its modules
(buffer, readers, signals, collector) directly."""

import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))
