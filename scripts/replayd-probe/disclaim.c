// Runs argv[1..] in place (same pid, same stdio) as its own TCC responsible
// process, the way a process launched by LaunchServices is.
#include <spawn.h>
#include <stdio.h>
extern int responsibility_spawnattrs_setdisclaim(posix_spawnattr_t *attrs, int disclaim);
extern char **environ;
int main(int argc, char **argv) {
    posix_spawnattr_t a;
    posix_spawnattr_init(&a);
    if (responsibility_spawnattrs_setdisclaim(&a, 1) != 0) { perror("disclaim"); return 1; }
    posix_spawnattr_setflags(&a, POSIX_SPAWN_SETEXEC);
    pid_t p;
    int rc = posix_spawn(&p, argv[1], NULL, &a, argv + 1, environ);
    perror("posix_spawn");
    return rc;
}
