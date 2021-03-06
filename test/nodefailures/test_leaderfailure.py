#!/usr/bin/env python2

import os
import sys
import unittest

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.append(os.path.dirname(_HERE))

import common

class LeaderFailureTest(common.TestCase):
    def test_leader_failure(self):
        """A leader client (1) is started followed by two normal clients (2, 3).
        The leader fails. 2 should become the new leader, and 3 should stay as a
        client.
        """
        ms_srv = common.MatchMakingServer(2222)
        ms_srv.start()
        common.sleep(2)

        clients = common.start_multiple_clients(ms_srv.port, 3)

        # XXX: This sleep is rather fragile because we need to resume after
        #      the MS has started the game, but before the game has ended.
        common.sleep(common.MatchMakingServer.GAME_START_TIMEOUT * 0.9)

        # Kill the leader.
        leader = clients[0]
        leader.kill()

        # Wait for leader re-election to occur.
        common.sleep(8)

        client2 = clients[1]
        with open(client2.local_log_path) as log_file:
            found_leader_msg = False
            found_node_msg = False
            found_node_msg_after_leader = False
            for line in log_file:
                if common.line_indicates_leader(line):
                    found_leader_msg = True
                    continue
                elif common.line_indicates_node(line):
                    found_node_msg = True
                    if found_leader_msg:
                        found_node_msg_after_leader = True
                    continue
            self.assertTrue(found_node_msg,
                            "Client 2 should have been a node at some point")
            self.assertTrue(found_leader_msg,
                            "Client 2 should become the leader")
            self.assertFalse(found_node_msg_after_leader,
                             "Client 2 should not have turn back into a node")
        client2.kill()

        client3 = clients[2]
        with open(client3.local_log_path) as log_file:
            found_leader_msg = False
            found_node_msg = False
            for line in log_file:
                if common.line_indicates_leader(line):
                    found_leader_msg = True
                    continue
                elif common.line_indicates_node(line):
                    found_node_msg = True
                    if found_leader_msg:
                        found_node_msg_after_leader = True
                    continue
            self.assertTrue(found_node_msg,
                            "Client 3 should have been a node at some point")
            self.assertFalse(found_leader_msg,
                             "Client 3 should stay as a normal node")

if __name__ == "__main__":
    print "Warning: this test case is fragile and requires precise timing."
    print "It may fail even if the implementation being tested is working."
    unittest.main()
