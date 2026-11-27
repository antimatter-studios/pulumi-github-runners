// The integration test's program: every component, deployed to a throwaway kind cluster by
// tests/integration/run.sh, which then checks the cluster with kubectl. Nothing here reaches GitHub:
// the credentials are dummies, so ARC's listener never starts and the gate agent's image never
// pulls from its registry (run.sh loads a stand-in under its name). The test checks what the plugin
// declares, not what GitHub answers.
"use strict";
const pulumi = require("@pulumi/pulumi");
const runners = require("../../sdk/nodejs");

const config = new pulumi.Config();
const manageSlots = config.requireBoolean("manageSlots");
const manageMaxRunners = config.requireBoolean("manageMaxRunners");

const controller = new runners.Controller("controller", {
    // A device that does not exist: the plugin's DaemonSet still runs and offers none of it.
    devices: [{ name: "fake", paths: ["/dev/integration-fake"], count: 4 }],
    // A small image, so the DaemonSet becomes ready in seconds rather than pulling the runner image.
    prePull: { images: ["busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"] },
    // Only the NetworkPolicy: the rest of monitoring needs the Prometheus Operator's CRDs. The
    // scraper it lists does not exist; the check is that the GateAgent is admitted beside it.
    monitoring: {
        networkPolicy: { allow: [{ namespaceLabels: { "kubernetes.io/metadata.name": "monitoring" } }] },
    },
});

const pool = new runners.Pool("pool", {
    poolId: "it",
    configUrl: "https://github.com/integration-test-org",
    scaleSetName: "kind-linux-x64",
    token: pulumi.secret("ghp_integration_dummy_token"),
    devices: { "devic.es/fake": "1" },
    networkPolicy: {},
}, { dependsOn: [controller] });

new runners.GateAgent("gate", {
    cluster: "kind",
    token: pulumi.secret("integration-dummy-agent-token"),
    image: "git.antimatter-studios.com/antimatter-studios/antimatter-vps-gate-webservice:0123456789abcdef",
    runnerNamespaces: ["arc-runners-it"],
    imagePull: { username: "integration", password: pulumi.secret("integration-dummy-password") },
    manageSlots,
    manageMaxRunners,
}, { dependsOn: [controller, pool] });

exports.poolNamespace = pool.namespace;
exports.controllerNamespace = controller.namespace;
