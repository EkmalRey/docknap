---
name: Bug report
about: Something isn't working as expected
title: "[bug] "
labels: bug
assignees: ''
---

## Describe the bug

A clear and concise description of what the bug is.

## To reproduce

Steps to reproduce the behavior:

1. Create a container with labels ...
2. Make a request to ...
3. See error ...

## Expected behavior

What you expected to happen.

## Actual behavior

What actually happened. Include any error messages verbatim.

## Environment

- docknap version: (output of `curl -s http://docknap:8000/_docknap/version | jq .version`)
- Docker version: (`docker --version`)
- Host OS:
- Architecture: (amd64 / arm64)
- docknap `docker-compose.yml` snippet:

```yaml
# paste relevant portion
```

## Labels on the affected container

```yaml
labels:
  - docknap.enable=...
  - docknap.subdomain=...
  - docknap.target_port=...
  - ...
```

## Logs

```text
$ docker logs docknap --tail=50
# paste output
```

## Additional context

Anything else that might be relevant.
