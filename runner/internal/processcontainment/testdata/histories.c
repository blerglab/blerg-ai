#define _GNU_SOURCE
#include <pthread.h>
#include <spawn.h>
extern char **environ;
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>
#include <fcntl.h>

static const char *marker;
static const char *mode;
static const char *executable;
static void mark_text(const char *text) {
    int fd = open(marker, O_WRONLY|O_CREAT|O_TRUNC, 0600);
    if (fd < 0 || write(fd, text, strlen(text)) != (ssize_t)strlen(text)) _exit(90);
    close(fd);
}
static void mark(void) { mark_text("ready"); }
static void forever(void) { for (;;) usleep(1000); }
static void heartbeat(void) {
    char path[4096], data[32]; unsigned long n = 0;
    snprintf(path, sizeof(path), "%s.beat", marker);
    for (;;) {
        char stop[4096];
        snprintf(stop, sizeof(stop), "%s.stop", marker);
        if (access(stop, F_OK) == 0) _exit(0);
        int fd = open(path, O_WRONLY|O_CREAT|O_TRUNC, 0600);
        int len = snprintf(data, sizeof(data), "%lu", ++n);
        if (fd < 0 || write(fd, data, len) != len) _exit(96);
        close(fd);
        if (strcmp(mode, "leader-exec") == 0) {
            snprintf(path, sizeof(path), "%s.exec", marker);
            if (access(path, F_OK) == 0) execl(executable, "histories", "exec-target", marker, NULL);
            snprintf(path, sizeof(path), "%s.beat", marker);
        }
        usleep(1000);
    }
}
static void *sleeper(void *arg) { (void)arg; forever(); return NULL; }
static int child(void *arg) {
    (void)arg;
    if (strcmp(mode, "vfork-exec") == 0) execl("/bin/true", "true", NULL);
    if (strcmp(mode, "vfork-delay") == 0) usleep(30000);
    _exit(0);
}
static int quick(void *arg) { (void)arg; _exit(0); }
static void *quick_thread(void *arg) { (void)arg; return NULL; }
// Short-lived processes and threads, created by a non-leader thread, exit before the tracer can
// collect their creator's FORK/VFORK/CLONE event; none may linger in the tracer's accounting.
static void storm(void) {
    char *stack = malloc(65536);
    if (!stack) _exit(97);
    for (int i = 0; i < 1500; i++) {
        pid_t pid = clone(quick, stack+65536, CLONE_VM|CLONE_VFORK|SIGCHLD, NULL);
        if (pid < 0 || waitpid(pid, NULL, 0) != pid) _exit(92);
        pid = fork();
        if (pid == 0) _exit(0);
        if (pid < 0 || waitpid(pid, NULL, 0) != pid) _exit(92);
        pthread_t t;
        if (pthread_create(&t, NULL, quick_thread, NULL) || pthread_join(t, NULL)) _exit(92);
    }
    free(stack);
}
static void *worker(void *arg) {
    (void)arg;
    if (strcmp(mode, "storm") == 0) storm();
    if (strcmp(mode, "posix-spawn") == 0) {
        for (int i = 0; i < 300; i++) {
            pid_t pid;
            char *args[] = {"true", NULL};
            if (posix_spawn(&pid, "/bin/true", NULL, NULL, args, environ) || waitpid(pid, NULL, 0) != pid) _exit(92);
        }
    }
    if (strncmp(mode, "leader-", 7) == 0) { usleep(100000); mark(); heartbeat(); }
    if (strcmp(mode, "exec-thread") == 0) {
        execl(executable, "histories", "exec-target", marker, NULL);
        _exit(91);
    }
    if (strncmp(mode, "vfork-", 6) == 0) {
        mark();
        char *stack = malloc(65536);
        if (!stack) _exit(97);
        for (int i = 0; i < 100; i++) {
            pid_t pid = clone(child, stack+65536, CLONE_VM|CLONE_VFORK|SIGCHLD, NULL);
            if (pid < 0 || waitpid(pid, NULL, 0) != pid) _exit(92);
        }
        free(stack);
    }
    mark();
    return NULL;
}
int main(int argc, char **argv) {
    if (argc != 3) return 93;
    executable = argv[0]; mode = argv[1]; marker = argv[2];
    if (strcmp(mode, "exec-target") == 0) { mark_text("exec-ready"); heartbeat(); }
    if (strcmp(mode, "orphan") == 0) {
        pid_t pid = fork();
        if (pid == 0) { if (fork() == 0) { usleep(100000); mark(); heartbeat(); } _exit(0); }
        if (pid < 0 || waitpid(pid, NULL, 0) != pid) return 94;
        forever();
    }
    pthread_t thread;
    // Exec must replace a group with several siblings, not just the leader and caller.
    if (strcmp(mode, "exec-thread") == 0 || strcmp(mode, "leader-exec") == 0) {
        pthread_t sibling;
        if (pthread_create(&sibling, NULL, sleeper, NULL)) return 98;
    }
    if (pthread_create(&thread, NULL, worker, NULL)) return 95;
    if (strncmp(mode, "leader-", 7) == 0) pthread_exit(NULL);
    if (strcmp(mode, "exec-thread") != 0) { pthread_join(thread, NULL); heartbeat(); }
    forever();
}
