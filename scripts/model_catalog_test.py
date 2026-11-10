import unittest

import model_catalog as mc


def doc(*models, schema=1):
    return {"schema_version": schema, "issued_at": "2026-01-02T03:04:05Z", "version": 7,
            "surfaces": {"cc": {"model_selector_config": [{"id": "cc", "models": list(models)}]},
                         "ccr": {"model_selector_config": [{"id": "ccr", "models": [{"id": "other", "name": "Other"}]}]}}}


class LocalProxy(unittest.TestCase):
    def test_takes_only_a_proxy_on_this_machine(self):
        self.assertEqual(mc.local_proxy("http://127.0.0.1:7890", {}), ("127.0.0.1", 7890))
        self.assertEqual(mc.local_proxy("http://localhost:8080", {}), ("localhost", 8080))
        self.assertEqual(mc.local_proxy("http://[::1]:8080", {}), ("::1", 8080))
        self.assertEqual(mc.local_proxy(None, {"https_proxy": "http://127.0.0.2:1"}), ("127.0.0.2", 1))
        for bad in ("http://10.0.0.5:7890", "http://proxy.example.com:7890", "socks5h://127.0.0.1:7891",
                    "https://127.0.0.1:7890", "http://127.0.0.1"):
            with self.assertRaises(mc.CatalogError, msg=bad):
                mc.local_proxy(bad, {})

    def test_no_proxy_is_an_error_not_a_direct_fetch(self):
        with self.assertRaises(mc.CatalogError):
            mc.local_proxy(None, {"HTTP_PROXY": "http://127.0.0.1:7890"})


class Trim(unittest.TestCase):
    def test_keeps_the_terminal_surface_and_the_fields_the_daemon_reads(self):
        out = mc.trim(doc(
            {"id": "a", "name": "A", "short_name": "A", "description": "d", "section": "main",
             "min_claude_code_version": "2.1.290", "offered_on": ["first_party"], "badge": {"x": 1},
             "runtime": {"effort_levels": ["low", "high"], "default_effort": "high", "max_input_tokens": 9, "family": "f"}},
            {"id": "b", "name": "B", "thinking": {"type": "effort", "effort_options": [{"id": "low"}, {"id": "max"}]},
             "auto_permission_mode": "unavailable", "disabled": True},
        ))
        self.assertEqual(out["issued_at"], "2026-01-02T03:04:05Z")
        self.assertEqual(out["models"], [
            {"id": "a", "name": "A", "short_name": "A", "description": "d", "section": "main",
             "min_claude_code_version": "2.1.290", "offered_on": ["first_party"],
             "runtime": {"effort_levels": ["low", "high"], "default_effort": "high", "max_input_tokens": 9}},
            {"id": "b", "name": "B", "auto_permission_mode": "unavailable", "disabled": True,
             "runtime": {"effort_levels": ["low", "max"]}},
        ])

    def test_refuses_an_unknown_format(self):
        for bad in (doc({"id": "a", "name": "A"}, schema=2), doc(), doc({"id": "a"})):
            with self.assertRaises(mc.CatalogError):
                mc.trim(bad)


class Diff(unittest.TestCase):
    def test_compares_rows_not_the_issue(self):
        a = {"issued_at": "1", "models": [{"id": "x", "name": "X"}, {"id": "y", "name": "Y"}]}
        self.assertEqual(mc.diff(a, dict(a, issued_at="2", version=9)), [])
        b = {"models": [{"id": "y", "name": "Y2"}, {"id": "z", "name": "Z"}]}
        self.assertEqual(mc.diff(a, b), ["+ z (Z)", "- x (X)", "~ y"])
        self.assertEqual(mc.diff(a, {"models": list(reversed(a["models"]))}), ["order: y, x"])
        self.assertEqual(mc.diff(None, a), ["+ x (X)", "+ y (Y)"])


if __name__ == "__main__":
    unittest.main()
