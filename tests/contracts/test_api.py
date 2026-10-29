"""T00-05: contract examples and Docker discovery snapshots, offline."""
import copy
import json
import re
import unittest
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = Path(__file__).parent / "fixtures"
API = json.loads((ROOT / "docs/api/openapi-v1.json").read_text(encoding="utf-8"))


def validate(name, obj):
    schema = {"$ref": f"#/components/schemas/{name}", "components": API["components"]}
    Draft202012Validator(schema, format_checker=FormatChecker()).validate(obj)


class ApiContractTests(unittest.TestCase):
    def test_openapi_schemas(self):
        for name, schema in API["components"]["schemas"].items():
            with self.subTest(schema=name):
                Draft202012Validator.check_schema(schema)

    def test_operations_are_secured_and_documented(self):
        ids = set()
        public = {("get", "/v1/health"), ("get", "/v1/identity"), ("post", "/v1/pairings")}
        for url, operations in API["paths"].items():
            for method, op in operations.items():
                self.assertNotIn(op["operationId"], ids)
                ids.add(op["operationId"])
                self.assertTrue(any(code.startswith("2") for code in op["responses"]), url)
                for name in re.findall(r"\{([^}]+)\}", url):
                    self.assertTrue(any(p.get("name") == name and p.get("in") == "path"
                                        for p in op.get("parameters", [])), url)
                if (method, url) in public:
                    self.assertEqual(op.get("security"), [])
                else:
                    self.assertNotEqual(op.get("security"), [])
                    refs = {p.get("$ref") for p in op.get("parameters", [])}
                    self.assertIn("#/components/parameters/AgentId", refs)
                    self.assertIn("#/components/parameters/ControllerId", refs)
        self.assertEqual(len(ids), 17)
        self.assertNotIn("/v1/catalog", API["paths"])

    def test_documented_json_examples(self):
        md = (ROOT / "docs/api/contract-v1.md").read_text(encoding="utf-8")
        fence = chr(96) * 3
        snippets = re.findall(re.escape(fence) + r"json\s*\n(.*?)\n" + re.escape(fence), md, re.S)
        types = ["Identity", "PairingRequest", "PairingResult",
                 "DockerInventory", "TaskStart", "Task", "TaskLogChunk"]
        self.assertEqual(len(snippets), len(types))
        for name, snippet in zip(types, snippets):
            with self.subTest(name=name):
                validate(name, json.loads(snippet))

    def test_portainer_sub2api_standalone_inventory(self):
        cases = {
            "portainer.json": ("compose_project", "portainer", 1),
            "sub2api.json": ("compose_project", "sub2api", 3),
            "standalone.json": ("container", None, 1),
        }
        for filename, expected in cases.items():
            with self.subTest(case=filename):
                snapshot = json.loads((FIXTURES / filename).read_text(encoding="utf-8"))
                validate("DockerInventory", snapshot)
                self.assertTrue(snapshot["complete"])
                self.assertEqual(snapshot["status"], "ok")
                self.assertEqual(len(snapshot["resources"]), 1)
                resource = snapshot["resources"][0]
                self.assertEqual(resource["kind"], expected[0])
                self.assertEqual(resource.get("compose_project"), expected[1])
                self.assertEqual(len(resource["containers"]), expected[2])
                self.assertEqual(len({c["container_id"] for c in resource["containers"]}), expected[2])

    def test_failed_docker_scan_never_implies_deletion(self):
        snapshot = json.loads((FIXTURES / "docker-error.json").read_text(encoding="utf-8"))
        validate("DockerInventory", snapshot)
        self.assertFalse(snapshot["complete"])
        self.assertEqual(snapshot["status"], "error")
        self.assertEqual(snapshot["resources"], [])
        authoritative_empty = snapshot["complete"] and snapshot["status"] == "ok" and not snapshot["resources"]
        self.assertFalse(authoritative_empty)

    def test_deploy_package_receipt_contract_is_storage_only(self):
        payload = {
            "app_id": "portainer",
            "git_revision": "a" * 40,
            "manifest_sha256": "b" * 64,
            "package_sha256": "c" * 64,
            "package_size": 1234,
            "file_count": 3,
            "stored_at": "2026-10-29T00:00:00Z",
            "ready_for_agent": False,
            "install_enabled": False,
            "reused": False,
        }
        validate("DeployPackageReceipt", payload)
        bad = dict(payload)
        bad["install_enabled"] = True
        with self.assertRaises(Exception):
            validate("DeployPackageReceipt", bad)
        self.assertIn("/v1/deploy/packages", API["paths"])
        self.assertIn("/v1/deploy/packages/{app_id}/{git_revision}", API["paths"])

    def test_task_allowlist_rejects_shell_and_invalid_pairing(self):
        task = {
            "task_id": "44444444-4444-4444-8444-444444444444",
            "kind": "application", "action": "install", "application_id": "portainer",
            "bundle_sha256": "b" * 64,
        }
        validate("TaskStart", task)
        bad = copy.deepcopy(task)
        bad["action"] = "run_shell"
        with self.assertRaises(Exception):
            validate("TaskStart", bad)
        pairing = {
            "controller_id": "22222222-2222-4222-8222-222222222222",
            "device_id": "33333333-3333-4333-8333-333333333333",
            "pairing_code": "EXAMPLE-ONE-TIME-CODE",
        }
        validate("PairingRequest", pairing)
        pairing["pairing_code"] = "123"
        with self.assertRaises(Exception):
            validate("PairingRequest", pairing)


if __name__ == "__main__":
    unittest.main()
