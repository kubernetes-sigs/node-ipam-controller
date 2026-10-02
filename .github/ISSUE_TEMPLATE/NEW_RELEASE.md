---
name: New Release
about: Propose and track a new release
title: Release v$MAJ.$MIN.$PATCH
labels: ''
assignees: ''
---

## Release Checklist

<!--
Please do not remove items from the checklist
-->

Replace `$MAJ`, `$MIN`, `$PATCH` in the commands. See
[MAINTENANCE.md](https://github.com/kubernetes-sigs/node-ipam-controller/blob/main/MAINTENANCE.md#release--promotion-process)
for details on each step.

- [ ] Fill in the "Changelog" section in this issue
  - Tip: use `https://github.com/kubernetes-sigs/node-ipam-controller/compare/<previous tag>...main`
- [ ] Get `/lgtm` from at least one other approver in [OWNERS](https://github.com/kubernetes-sigs/node-ipam-controller/blob/main/OWNERS)
- [ ] Check that `main` is green in GitHub Actions and on [TestGrid](https://testgrid.k8s.io/sig-network-node-ipam-controller)
- [ ] Push the release tag
  - [ ] `git tag -s v$MAJ.$MIN.$PATCH -m "Release v$MAJ.$MIN.$PATCH"`
  - [ ] `git push upstream v$MAJ.$MIN.$PATCH`
- [ ] Create a draft release with `gh release create v$MAJ.$MIN.$PATCH --draft --verify-tag --generate-notes --notes-start-tag <previous tag>` and edit the notes
- [ ] Check the staging artifacts and paste the digests in a comment in this issue
  - [ ] `crane digest gcr.io/k8s-staging-networking/node-ipam-controller:v$MAJ.$MIN.$PATCH`
  - [ ] `crane digest gcr.io/k8s-staging-networking/charts/node-ipam-controller:$MAJ.$MIN.$PATCH`
- [ ] Submit a PR against [k8s.io](https://github.com/kubernetes/k8s.io) to update [images.yaml](https://github.com/kubernetes/k8s.io/blob/main/registry.k8s.io/images/k8s-staging-networking/images.yaml)
  - The chart tag has no `v` prefix
  - PR: \<Insert your PR here\>
- [ ] Verify that the image and chart have been promoted
  - [ ] `crane digest registry.k8s.io/networking/node-ipam-controller:v$MAJ.$MIN.$PATCH`
  - [ ] `helm show chart oci://registry.k8s.io/networking/charts/node-ipam-controller --version $MAJ.$MIN.$PATCH`
- [ ] Publish the draft release
  - Release: \<Insert your release here\>
- [ ] Update `--version` in the [README install command](https://github.com/kubernetes-sigs/node-ipam-controller/blob/main/README.md#installation) to `$MAJ.$MIN.$PATCH`
  - PR: \<Insert your PR here\>
- [ ] Announce the release in [#sig-network](https://kubernetes.slack.com/messages/sig-network) and on the [SIG Network mailing list](https://groups.google.com/a/kubernetes.io/g/sig-network) with the subject `[ANNOUNCE] node-ipam-controller v$MAJ.$MIN.$PATCH is released`
  - Announcement: \<Insert your announcement here\>
- [ ] Close this issue

## Changelog

<!--
Describe changes since the last release here.
-->
