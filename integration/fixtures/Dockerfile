# Minimal build fixture for the integration build test. It builds from
# scratch with a statically linked helper binary so BuildKit never needs
# registry access, and the built image stays runnable.
FROM scratch

COPY marker.txt /dls-marker.txt
COPY dls-helper /dls-helper

ENTRYPOINT ["/dls-helper"]
