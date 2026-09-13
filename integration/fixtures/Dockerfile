# Minimal build fixture for the integration build and compatibility tests. It
# builds FROM scratch with a statically linked helper binary so BuildKit never
# needs registry access, and the built image stays runnable. EXPOSE lets
# testcontainers-go derive published ports from ImageInspect.Config.
FROM scratch

COPY marker.txt /dls-marker.txt
COPY dls-helper /dls-helper

EXPOSE 8080/tcp
ENV DLS_COMPAT_ENV=present
ENTRYPOINT ["/dls-helper"]
CMD ["serve", "8080"]
