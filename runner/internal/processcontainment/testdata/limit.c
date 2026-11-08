#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <pthread.h>
#include <sched.h>
#include <signal.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <sys/wait.h>
#include <unistd.h>

// Runs under the task limit: the root stays out of the way and a worker (not the root's process, so
// it has no reserve) does the mode. One result line per fact goes to <marker>; <marker>.done ends it.
static const char *marker;
static char self[4096];

static void say(const char *format, ...) __attribute__((format(printf, 1, 2)));
static void say(const char *format, ...) {
    char line[256];
    va_list args;
    va_start(args, format);
    int n = vsnprintf(line, sizeof(line), format, args);
    va_end(args);
    int fd = open(marker, O_WRONLY|O_CREAT|O_APPEND, 0600);
    if (fd < 0 || write(fd, line, n) != n) _exit(90);
    close(fd);
}
static void flag(const char *suffix) {
    char path[4096];
    snprintf(path, sizeof(path), "%s.%s", marker, suffix);
    close(open(path, O_WRONLY|O_CREAT, 0600));
}
static int flagged(const char *suffix) {
    char path[4096];
    snprintf(path, sizeof(path), "%s.%s", marker, suffix);
    return access(path, F_OK) == 0;
}

enum kind { FORK, VFORK, CLONE, THREAD };
static const char *names[] = {"fork", "vfork", "clone", "thread"};
static void *sleeper(void *arg) { (void)arg; for (;;) pause(); return NULL; }

// create starts one task that stays until killed. 0 on success, else the errno.
static int create(enum kind kind, pid_t *pid) {
    pid_t got;
    switch (kind) {
    case FORK:
#ifdef SYS_fork
        got = syscall(SYS_fork);
#else
        got = syscall(SYS_clone, SIGCHLD, 0, 0, 0, 0);
#endif
        break;
    case VFORK:
        got = vfork();
        if (got == 0) { char *argv[] = {"limit", "stay", NULL}; execv(self, argv); _exit(1); }
        break;
    case CLONE:
        got = syscall(SYS_clone, SIGUSR1, 0, 0, 0, 0); // not SIGCHLD: the kernel's CLONE event
        break;
    default: {
        pthread_t thread;
        int e = pthread_create(&thread, NULL, sleeper, NULL);
        *pid = 0;
        return e;
    }
    }
    if (got < 0) return errno;
    if (got == 0) for (;;) pause();
    *pid = got;
    return 0;
}

// fill creates tasks of one kind until refused, and says how many and why. Threads die with this
// process, so they are counted from a process of their own.
static int fill(enum kind kind, pid_t *pids, int *e) {
    int n = 0;
    for (;;) {
        *e = create(kind, &pids[n]);
        if (*e != 0) return n;
        n++;
        if (n >= 16384) return n;
    }
}
static void drain(pid_t *pids, int n) {
    for (int i = 0; i < n; i++) if (pids[i] > 0) kill(pids[i], SIGKILL);
    for (int i = 0; i < n; i++) if (pids[i] > 0) waitpid(pids[i], NULL, __WALL);
    usleep(50000); // the tracer drops an entry just after the real parent's wait can return
}
static pid_t pids[16384];

static int roomy(void) {
    int e, n = fill(FORK, pids, &e);
    drain(pids, n);
    return n;
}

static void each_kind(void) {
    for (int round = 0; round < 3; round++) {
        for (int kind = FORK; kind <= THREAD; kind++) {
            pid_t holder = kind == THREAD ? fork() : 0;
            if (holder < 0) _exit(91);
            if (holder > 0) { waitpid(holder, NULL, 0); usleep(50000); continue; }
            int e, n = fill(kind, pids, &e);
            say("%s created=%d errno=%d\n", names[kind], n + (kind == THREAD), e);
            if (kind == THREAD) _exit(0);
            drain(pids, n);
        }
    }
}

// A failed creation has no creation event; its reservation ends at the syscall-exit stop.
static void failed_clones(void) {
    for (int i = 0; i < 3000; i++) {
        long r = syscall(SYS_clone, CLONE_THREAD, 0, 0, 0, 0); // EINVAL without CLONE_SIGHAND
        if (r != -1 || errno != EINVAL) { say("clone returned %ld errno=%d\n", r, errno); _exit(92); }
    }
    say("after-failures created=%d\n", roomy());
}

static void on_alarm(int s) { (void)s; }
// A pending signal fails copy_process with ERESTARTNOINTR; the restart comes through the stop again.
static void restarts(void) {
    struct sigaction action = {.sa_handler = on_alarm};
    sigaction(SIGALRM, &action, NULL);
    struct itimerval every = {{0, 500}, {0, 500}};
    setitimer(ITIMER_REAL, &every, NULL);
    for (int i = 0; i < 500; i++) {
        pid_t pid = fork();
        if (pid == 0) _exit(0);
        if (pid < 0) { say("fork failed errno=%d\n", errno); _exit(93); }
        while (waitpid(pid, NULL, 0) < 0 && errno == EINTR) {}
    }
    struct itimerval off = {{0, 0}, {0, 0}};
    setitimer(ITIMER_REAL, &off, NULL);
    usleep(50000);
    say("after-restarts created=%d\n", roomy());
}

// Tasks die at every point of a creation, reservations included.
static void killed_in_clone(void) {
    pid_t forkers[8];
    for (int round = 0; round < 20; round++) {
        for (int i = 0; i < 8; i++) {
            forkers[i] = fork();
            if (forkers[i] < 0) _exit(94);
            if (forkers[i] == 0) {
                for (;;) {
                    pid_t pid = fork();
                    if (pid == 0) _exit(0);
                    if (pid > 0) waitpid(pid, NULL, 0);
                }
            }
        }
        usleep(2000);
        drain(forkers, 8);
    }
    say("after-kills created=%d\n", roomy());
}

static volatile int full;
static void *spawner(void *arg) {
    (void)arg;
    for (;;) {
        pthread_t thread;
        if (pthread_create(&thread, NULL, sleeper, NULL) == 0) pthread_detach(thread);
        else full = 1;
    }
    return NULL;
}
static void *execer(void *arg) {
    (void)arg;
    for (int i = 0; !full && i < 10000; i++) usleep(100);
    char *argv[] = {"limit", "exit", NULL};
    execv(self, argv);
    _exit(1);
}
// A sibling's exec kills threads in the middle of their creations, the tree at its limit: from the
// leader, and from a thread, which runs the exec barrier with the others held at their creating stops.
static void exec_over_clones(void) {
    for (int round = 0; round < 40; round++) {
        pid_t pid = fork();
        if (pid < 0) _exit(95);
        if (pid == 0) {
            pthread_t thread;
            pthread_create(&thread, NULL, spawner, NULL);
            pthread_create(&thread, NULL, spawner, NULL);
            if (round % 2 == 0) execer(NULL);
            pthread_create(&thread, NULL, execer, NULL);
            pause();
        }
        waitpid(pid, NULL, 0);
    }
    usleep(50000);
    say("after-exec created=%d\n", roomy());
}

// A filter of the tree's own answers TRACE too, with data of its choosing: 0, the exec filter's, on
// clone and 1 on getppid. Neither may decide anything.
static void own_trace_filter(void) {
    struct sock_filter filter[] = {
        BPF_STMT(BPF_LD|BPF_W|BPF_ABS, offsetof(struct seccomp_data, nr)),
        BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, SYS_clone, 0, 1),
        BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_TRACE|0),
        BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, SYS_getppid, 0, 1),
        BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_TRACE|1),
        BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ALLOW),
    };
    struct sock_fprog program = {sizeof(filter)/sizeof(filter[0]), filter};
    if (prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program)) { say("filter errno=%d\n", errno); _exit(96); }
    long parent = syscall(SYS_getppid);
    int e, n = fill(FORK, pids, &e);
    say("own-filter created=%d errno=%d parent=%d\n", n, e, parent > 0);
    drain(pids, n);
}

static void bomb(void) {
    flag("bombing");
    for (;;) fork();
}

// Fills the tree from below while the root's own process goes on creating threads into its reserve.
static void reserve_worker(void) {
    int e, n = fill(FORK, pids, &e);
    say("worker created=%d errno=%d\n", n, e);
    flag("full");
    while (!flagged("done")) usleep(1000);
    drain(pids, n);
}
// Leaves the whole tree to the root, which fills it by itself.
static void engine_worker(void) {
    flag("full");
    while (!flagged("done")) usleep(1000);
}
// The root's children take the tree to the end of the reserve, and its threads go on past that.
static void reserve_root(void) {
    while (!flagged("full")) usleep(1000);
    int n = 0, e;
    pid_t child;
    while ((child = fork()) > 0) n++;
    if (child == 0) for (;;) pause();
    say("root-children created=%d errno=%d\n", n, errno);
    n = 0;
    pthread_t thread;
    while ((e = pthread_create(&thread, NULL, sleeper, NULL)) == 0) n++;
    say("root-threads created=%d errno=%d\n", n, e);
    flag("done");
}

// One step of a working engine: a thread, as a runtime needs one at any moment, and a tool, which at
// the end of the reserve is refused. A refused thread ends the engine, as it does a Go program.
static void *step(void *arg) { return arg; }
static void engine_step(void) {
    pthread_t thread;
    if (pthread_create(&thread, NULL, step, NULL) != 0) _exit(98);
    pthread_join(thread, NULL);
    pid_t tool = fork();
    if (tool == 0) for (;;) pause();
    if (tool < 0) flag("reserve-spent");
}

int main(int argc, char **argv) {
    if (argc > 1 && strcmp(argv[1], "stay") == 0) for (;;) pause();
    if (argc > 1 && strcmp(argv[1], "exit") == 0) _exit(0);
    if (argc < 3) return 2;
    marker = argv[2];
    if (readlink("/proc/self/exe", self, sizeof(self) - 1) < 0) return 2;
    signal(SIGUSR1, SIG_IGN);
    const char *mode = argv[1];
    pid_t worker = fork();
    if (worker < 0) return 3;
    if (worker == 0) {
        if (!strcmp(mode, "each-kind")) each_kind();
        else if (!strcmp(mode, "failed-clones")) failed_clones();
        else if (!strcmp(mode, "restarts")) restarts();
        else if (!strcmp(mode, "killed-in-clone")) killed_in_clone();
        else if (!strcmp(mode, "exec-over-clones")) exec_over_clones();
        else if (!strcmp(mode, "own-trace-filter")) own_trace_filter();
        else if (!strcmp(mode, "reserve")) reserve_worker();
        else if (!strcmp(mode, "engine-fill")) engine_worker();
        else if (!strcmp(mode, "bomb")) bomb();
        else _exit(4);
        _exit(0);
    }
    if (!strcmp(mode, "reserve") || !strcmp(mode, "engine-fill")) reserve_root();
    if (!strcmp(mode, "bomb")) {
        // The root is the engine: it goes on working whatever the tree does, until it is told.
        for (unsigned long beat = 0; !flagged("done"); beat++) {
            char text[32];
            snprintf(text, sizeof(text), "%lu", beat);
            char path[4096];
            snprintf(path, sizeof(path), "%s.beat", marker);
            int fd = open(path, O_WRONLY|O_CREAT|O_TRUNC, 0600);
            if (fd < 0 || write(fd, text, strlen(text)) < 0) _exit(97);
            close(fd);
            engine_step();
            usleep(1000);
        }
        kill(-getpgrp(), SIGKILL);
        return 0;
    }
    int status;
    while (waitpid(worker, &status, 0) < 0 && errno == EINTR) {}
    say("worker status=%#x\n", status);
    flag("finished");
    while (!flagged("census")) usleep(1000); // the tracer's books are read with the root still here
    return 7;
}
