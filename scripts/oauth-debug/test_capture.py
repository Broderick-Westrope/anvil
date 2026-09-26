import importlib.util
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch


class CaptureTests(unittest.TestCase):
    def test_null_json_body_does_not_interrupt_capture(self):
        mitmproxy = types.ModuleType("mitmproxy")
        mitmproxy.http = types.ModuleType("mitmproxy.http")
        mitmproxy.http.HTTPFlow = type("HTTPFlow", (), {})
        mitmproxy.ctx = types.SimpleNamespace(log=types.SimpleNamespace(info=lambda message: None))
        with patch.dict(sys.modules, {"mitmproxy": mitmproxy}):
            spec = importlib.util.spec_from_file_location("oauth_capture", Path(__file__).with_name("capture.py"))
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            request = types.SimpleNamespace(
                pretty_host="api.anthropic.com",
                content=b"null",
                headers={},
                query={},
                method="POST",
                url="https://api.anthropic.com/v1/messages",
                path="/v1/messages",
            )
            module.request(types.SimpleNamespace(request=request))
            self.assertEqual(1, len(module._captures))
            self.assertEqual({"_non_object_body": True}, module._captures[0]["body"])


if __name__ == "__main__":
    unittest.main()
