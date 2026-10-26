"""T00-05: run the COMPLETE proposed T00-04 DDL against an in-memory legacy-shaped SQLite fixture."""
import sqlite3
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SQL = (ROOT / "docs/schema/T00-04-proposed.sql").read_text(encoding="utf-8")

BASE = """
CREATE TABLE users(id INTEGER PRIMARY KEY);
CREATE TABLE devices(user_id INTEGER NOT NULL, machine_id TEXT NOT NULL,
 PRIMARY KEY(user_id,machine_id), FOREIGN KEY(user_id) REFERENCES users(id));
CREATE TABLE asset_devices(id TEXT PRIMARY KEY,owner_user_id INTEGER NOT NULL,machine_id TEXT NOT NULL,
 UNIQUE(id,owner_user_id,machine_id),
 FOREIGN KEY(owner_user_id,machine_id) REFERENCES devices(user_id,machine_id) ON DELETE RESTRICT);
CREATE TABLE asset_services(id TEXT PRIMARY KEY,owner_user_id INTEGER NOT NULL,machine_id TEXT NOT NULL,
 device_id TEXT NOT NULL, UNIQUE(id,owner_user_id,machine_id,device_id),
 FOREIGN KEY(device_id,owner_user_id,machine_id)
 REFERENCES asset_devices(id,owner_user_id,machine_id) ON DELETE CASCADE);
CREATE TABLE deploy_agents(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,credential TEXT NOT NULL,
 agent_id TEXT,FOREIGN KEY(user_id) REFERENCES users(id));
CREATE TABLE deploy_tasks(task_id TEXT NOT NULL,connection_id TEXT NOT NULL,user_id INTEGER NOT NULL,
 PRIMARY KEY(connection_id,task_id),FOREIGN KEY(connection_id) REFERENCES deploy_agents(id) ON DELETE CASCADE);
"""

SAMPLE = """
INSERT INTO users VALUES(1); INSERT INTO users VALUES(2);
INSERT INTO devices(user_id,machine_id) VALUES(1,'node1');
INSERT INTO asset_devices(id,owner_user_id,machine_id) VALUES('device1',1,'node1');
INSERT INTO asset_services(id,owner_user_id,machine_id,device_id) VALUES('svc1',1,'node1','device1');
INSERT INTO deploy_agents(id,user_id,credential,agent_id) VALUES('host1',1,'ENCRYPTED','agent1');
INSERT INTO deploy_agents(id,user_id,credential,agent_id) VALUES('host2',2,'ENCRYPTED','agent2');
INSERT INTO deploy_tasks(task_id,connection_id,user_id) VALUES('task1','host1',1);
INSERT INTO host_agent_bindings(connection_id,owner_user_id,agent_id,machine_id,asset_device_id,
 binding_state,tls_spki_sha256,paired_at,updated_at)
 VALUES('host1',1,'agent1','node1','device1','bound',
 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2026-10-27','2026-10-27');
INSERT INTO host_app_instances(id,host_connection_id,owner_user_id,app_id,source_kind,
 software_root_at_install,management_state,created_at,updated_at)
 VALUES('app1','host1',1,'portainer','deploy','/software','managed','2026-10-27','2026-10-27');
INSERT INTO host_app_service_links(instance_id,owner_user_id,service_id,machine_id,asset_device_id,
 is_primary,link_origin,created_at,updated_at)
 VALUES('app1',1,'svc1','node1','device1',1,'automatic','2026-10-27','2026-10-27');
"""


class ProposedSqliteModelTests(unittest.TestCase):
    def setUp(self):
        self.db = sqlite3.connect(":memory:")
        self.db.execute("PRAGMA foreign_keys=ON")
        self.db.executescript(BASE)
        self.db.executescript(SQL)
        self.db.executescript(SAMPLE)

    def tearDown(self):
        self.db.close()

    def count(self, name):
        return self.db.execute("SELECT COUNT(*) FROM " + name).fetchone()[0]

    def test_actual_proposed_ddl_runs_and_foreign_keys_hold(self):
        self.assertEqual(self.db.execute("PRAGMA foreign_keys").fetchone()[0], 1)
        self.assertEqual(self.db.execute("PRAGMA foreign_key_check").fetchall(), [])

    def test_archive_and_unbind_preserve_service_app_and_history(self):
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("DELETE FROM asset_devices WHERE id='device1'")
        self.db.execute("UPDATE asset_devices SET archived_at='2026-10-27' WHERE id='device1'")
        self.db.execute("UPDATE devices SET inventory_archived_at='2026-10-27' WHERE machine_id='node1'")
        self.db.execute("""UPDATE host_agent_bindings SET binding_state='unbound',
            machine_id=NULL,asset_device_id=NULL,
            last_machine_id='node1',last_asset_device_id='device1',unbound_at='2026-10-27'
            WHERE connection_id='host1'""")
        self.db.execute("UPDATE deploy_agents SET credential='' WHERE id='host1'")
        for table in ("deploy_agents", "deploy_tasks", "host_app_instances",
                      "asset_services", "host_app_service_links"):
            with self.subTest(table=table):
                self.assertGreater(self.count(table), 0)
        self.assertEqual(self.db.execute("PRAGMA foreign_key_check").fetchall(), [])

    def test_cross_owner_app_relationship_is_refused(self):
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("""INSERT INTO host_app_instances(id,host_connection_id,
                owner_user_id,app_id,source_kind,software_root_at_install,management_state,
                created_at,updated_at) VALUES('bad','host1',2,'bad','deploy','/software',
                'managed','a','a')""")

    def test_duplicate_application_is_refused(self):
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("""INSERT INTO host_app_instances(id,host_connection_id,
                owner_user_id,app_id,source_kind,software_root_at_install,management_state,
                created_at,updated_at) VALUES('dup','host1',1,'portainer','deploy',
                '/software','managed','a','a')""")

    def test_duplicate_agent_identity_is_refused(self):
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("""INSERT INTO host_agent_bindings(connection_id,owner_user_id,
              agent_id,binding_state,tls_spki_sha256,updated_at)
              VALUES('host2',2,'agent1','unbound',
              'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','a')""")

    def test_only_one_primary_service_and_service_scope(self):
        self.db.execute("""INSERT INTO asset_services(id,owner_user_id,machine_id,device_id)
                         VALUES('svc2',1,'node1','device1')""")
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("""INSERT INTO host_app_service_links(instance_id,owner_user_id,
              service_id,machine_id,asset_device_id,is_primary,link_origin,created_at,updated_at)
              VALUES('app1',1,'svc2','node1','device1',1,'user','a','a')""")
        self.db.execute("""INSERT INTO host_app_service_links(instance_id,owner_user_id,
          service_id,machine_id,asset_device_id,is_primary,link_origin,created_at,updated_at)
          VALUES('app1',1,'svc2','node1','device1',0,'user','a','a')""")
        self.assertEqual(self.count("host_app_service_links"), 2)
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("DELETE FROM host_app_instances WHERE id='app1'")

    def test_deleting_one_service_only_cascades_its_link(self):
        self.db.execute("DELETE FROM asset_services WHERE id='svc1'")
        self.assertEqual(self.count("host_app_service_links"), 0)
        self.assertEqual(self.count("host_app_instances"), 1)
        self.assertEqual(self.db.execute("PRAGMA foreign_key_check").fetchall(), [])

    def test_deleting_host_would_destroy_task_but_is_blocked(self):
        with self.assertRaises(sqlite3.IntegrityError):
            self.db.execute("DELETE FROM deploy_agents WHERE id='host1'")
        self.assertEqual(self.count("deploy_tasks"), 1)


if __name__ == "__main__":
    unittest.main()
