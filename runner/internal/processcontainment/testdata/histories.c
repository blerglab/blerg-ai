#define _GNU_SOURCE
#include <pthread.h>
#include <spawn.h>
extern char **environ;
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <stddef.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <linux/filter.h>
#include <linux/seccomp.h>

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
static void path_for(char *path, size_t size, const char *suffix) { snprintf(path, size, "%s%s", marker, suffix); }
static void write_pid(const char *suffix, pid_t pid) {
    char path[4096], data[32];
    path_for(path, sizeof(path), suffix);
    int fd = open(path, O_WRONLY|O_CREAT|O_TRUNC, 0600);
    int len = snprintf(data, sizeof(data), "%d", pid);
    if (fd < 0 || write(fd, data, len) != len) _exit(90);
    close(fd);
}
// Each new child's first instruction appends one byte: a byte in the file is a child that ran.
// The test's tracer continues no child, so nothing may ever arrive, whatever SIGCONT does.
static void *sigcont_storm(void *arg) { (void)arg; for (;;) kill(0, SIGCONT); }
static void sigcont_children(void) {
    char path[4096];
    path_for(path, sizeof(path), ".ran");
    int fd = open(path, O_WRONLY|O_CREAT|O_APPEND, 0600);
    if (fd < 0) _exit(90);
    if (strcmp(mode, "sigcont-children-blocked") == 0) {
        sigset_t set;
        sigemptyset(&set);
        sigaddset(&set, SIGCONT);
        if (sigprocmask(SIG_BLOCK, &set, NULL)) _exit(90);
    }
    pthread_t storm;
    if (pthread_create(&storm, NULL, sigcont_storm, NULL)) _exit(95);
    for (int i = 0; i < 400; i++) {
        pid_t pid = fork();
        if (pid == 0) { if (write(fd, "x", 1) != 1) _exit(90); _exit(0); }
        if (pid < 0) _exit(92);
    }
    mark();
    forever();
}
// A long stay in the kernel between beats: a stop signal stays pending all that time, for SIGCONT to
// discard. SIGCONT is blocked, so its own delivery is no stop either: only the freeze can stop this.
static void kernel_beat(void) {
    char path[4096], data[32];
    sigset_t set;
    sigemptyset(&set);
    sigaddset(&set, SIGCONT);
    if (sigprocmask(SIG_BLOCK, &set, NULL)) _exit(90);
    path_for(path, sizeof(path), ".beat");
    write_pid(".pid", getpid());
    mark();
    for (unsigned long n = 1;; n++) {
        size_t size = 256UL << 20;
        void *p = mmap(NULL, size, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS|MAP_POPULATE, -1, 0);
        if (p == MAP_FAILED) _exit(90);
        munmap(p, size);
        int fd = open(path, O_WRONLY|O_CREAT|O_TRUNC, 0600);
        int len = snprintf(data, sizeof(data), "%lu", n);
        if (fd < 0 || write(fd, data, len) != len) _exit(96);
        close(fd);
    }
}
// A thread whose every exec fails, by both syscalls: the tree pauses for each and carries on.
static void *failing_execs(void *arg) {
    (void)arg;
    char *args[] = {"none", NULL};
    for (int i = 0; i < 20; i++) {
        execve("/nonexistent/path", args, environ);
        syscall(SYS_execveat, AT_FDCWD, "/nonexistent/path", args, environ, 0);
    }
    mark();
    forever();
    return NULL;
}
static void *execveat_thread(void *arg) {
    (void)arg;
    char *args[] = {"histories", "exec-target", (char *)marker, NULL};
    syscall(SYS_execveat, AT_FDCWD, executable, args, environ, 0);
    _exit(91);
}
// A filter of the tree's own on exec. The kernel takes the strictest answer of all filters, and
// USER_NOTIF outranks the tracer's TRACE: a listener that answers CONTINUE skips the exec stop.
static int exec_filter(unsigned action, unsigned flags) {
    struct sock_filter filter[] = {
        BPF_STMT(BPF_LD|BPF_W|BPF_ABS, offsetof(struct seccomp_data, nr)),
        BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, SYS_execve, 1, 0),
        BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, SYS_execveat, 0, 1),
        BPF_STMT(BPF_RET|BPF_K, action),
        BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ALLOW),
    };
    struct sock_fprog program = {sizeof(filter)/sizeof(filter[0]), filter};
    return syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, flags, &program);
}
static volatile int notify_fd = -1;
static void *listener_exec(void *arg) {
    (void)arg;
    int fd = exec_filter(SECCOMP_RET_USER_NOTIF, SECCOMP_FILTER_FLAG_NEW_LISTENER);
    write_pid(".listener", fd < 0 ? errno : 0);
    notify_fd = fd < 0 ? -2 : fd;
    execl(executable, "histories", "exec-target", marker, NULL);
    _exit(91);
}
// The cooperating listener: it passes the thread's exec straight on, unseen by the tracer.
static void listener_tree(void) {
    pthread_t t;
    if (pthread_create(&t, NULL, listener_exec, NULL)) _exit(95);
    while (notify_fd == -1) usleep(100);
    if (notify_fd >= 0) {
        struct seccomp_notif request;
        struct seccomp_notif_resp response;
        memset(&request, 0, sizeof(request));
        memset(&response, 0, sizeof(response));
        if (ioctl(notify_fd, SECCOMP_IOCTL_NOTIF_RECV, &request)) _exit(90);
        response.id = request.id;
        response.flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE;
        if (ioctl(notify_fd, SECCOMP_IOCTL_NOTIF_SEND, &response)) _exit(90);
    }
    forever();
}
// Filters that add nothing to the tracer's answer, then the same exec: the barrier still holds it.
static void *stacked_exec(void *arg) {
    (void)arg;
    if (exec_filter(SECCOMP_RET_ALLOW, 0) || exec_filter(SECCOMP_RET_LOG, SECCOMP_FILTER_FLAG_LOG)
        || exec_filter(SECCOMP_RET_ALLOW, SECCOMP_FILTER_FLAG_TSYNC) || exec_filter(SECCOMP_RET_TRACE|7, 0)) {
        write_pid(".listener", errno);
        _exit(90);
    }
    write_pid(".listener", 0);
    execl(executable, "histories", "exec-target", marker, NULL);
    _exit(91);
}
// One long stay in the kernel that no interrupt can end, while a thread of another process execs.
static void *late_exec(void *arg) {
    (void)arg;
    execl(executable, "histories", "exec-target", marker, NULL);
    _exit(91);
}
static void stuck_pause(void) {
    if (fork() == 0) {
        size_t size = 2048UL << 20;
        usleep(20000);
        void *p = mmap(NULL, size, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS|MAP_POPULATE, -1, 0);
        if (p == MAP_FAILED) _exit(90);
        munmap(p, size);
        forever();
    }
    usleep(40000);
    pthread_t t;
    if (pthread_create(&t, NULL, late_exec, NULL)) _exit(95);
    forever();
}
// Everything at once from several threads, until told to stop: processes, vfork, threads, execs by
// leaders and by threads, a process stopped and resumed by the tree, SIGCONT at the whole group.
static void *chaos_exec(void *arg) { (void)arg; execl(executable, "histories", "exit-now", marker, NULL); _exit(91); }
static void *chaos_worker(void *arg) {
    unsigned seed = (unsigned)(long)arg;
    char *stack = malloc(65536);
    if (!stack) _exit(97);
    char stop[4096];
    path_for(stop, sizeof(stop), ".stop");
    for (;;) {
        pid_t pid = -1;
        pthread_t t;
        if (access(stop, F_OK) == 0) return NULL;
        switch (rand_r(&seed) % 7) {
        case 0: pid = fork(); if (pid == 0) _exit(0); break;
        case 1: pid = clone(quick, stack+65536, CLONE_VM|CLONE_VFORK|SIGCHLD, NULL); break;
        case 2: if (!pthread_create(&t, NULL, quick_thread, NULL)) pthread_join(t, NULL); break;
        case 3: pid = vfork(); if (pid == 0) { execl("/bin/true", "true", NULL); _exit(0); } break;
        case 4: pid = fork(); if (pid == 0) { if (!pthread_create(&t, NULL, chaos_exec, NULL)) forever(); _exit(0); } break;
        case 5:
            pid = fork();
            if (pid == 0) { kill(getpid(), SIGSTOP); _exit(0); }
            if (pid > 0) { int st; waitpid(pid, &st, WUNTRACED); kill(pid, SIGCONT); }
            break;
        case 6: kill(0, SIGCONT); break;
        }
        if (pid > 0) waitpid(pid, NULL, 0);
    }
}
static void chaos(void) {
    pthread_t t;
    for (long i = 1; i <= 4; i++) if (pthread_create(&t, NULL, chaos_worker, (void *)i)) _exit(95);
    mark();
    heartbeat();
}
// The child stops itself; its parent sees the stop. The beat starts only after a SIGCONT from outside.
static void self_stop(void) {
    pid_t pid = fork();
    if (pid == 0) { kill(getpid(), SIGSTOP); heartbeat(); }
    if (pid < 0) _exit(92);
    write_pid(".pid", pid);
    int status;
    if (waitpid(pid, &status, WUNTRACED) != pid || !WIFSTOPPED(status)) _exit(92);
    mark_text("ready");
    forever();
}
// TID reuse across a pending exec. Run inside a PID namespace, so ns_last_pid is writable. In each
// round a non-leader thread T of a fresh process publishes its TID and execs, while reusers aim every
// fork at T's number. A child that lands on it stays alive and counts, for the freeze to stop.
struct reuse { volatile int tid, stop, hits; volatile unsigned long beat; };
static struct reuse *shared;
static int reuser(void *arg) {
    (void)arg;
    int last = open("/proc/sys/kernel/ns_last_pid", O_WRONLY);
    if (last < 0) _exit(90);
    while (!shared->stop) {
        int tid = shared->tid;
        char data[32];
        if (tid < 2) continue;
        int len = snprintf(data, sizeof(data), "%d", tid-1);
        if (pwrite(last, data, len, 0) != len) _exit(90);
        pid_t pid = fork();
        if (pid == 0) {
            if ((int)syscall(SYS_gettid) != tid) _exit(0);
            __sync_fetch_and_add(&shared->hits, 1);
            for (;;) { __sync_fetch_and_add(&shared->beat, 1); usleep(1000); }
        }
        if (pid > 0) while (waitpid(-1, NULL, WNOHANG) > 0);
    }
    _exit(0);
}
static void *reuse_thread(void *arg) {
    (void)arg;
    shared->tid = (int)syscall(SYS_gettid);
    execl(executable, "histories", "exit-now", marker, NULL);
    _exit(91);
}
static void tid_reuse(void) {
    shared = mmap(NULL, 4096, PROT_READ|PROT_WRITE, MAP_SHARED|MAP_ANONYMOUS, -1, 0);
    if (shared == MAP_FAILED) _exit(90);
    for (int i = 0; i < 4; i++) if (fork() == 0) reuser(NULL);
    int rounds = strcmp(mode, "tid-reuse-short") == 0 ? 200 : 1500;
    char budget[4096];
    path_for(budget, sizeof(budget), ".stop");
    int round = 0;
    for (; round < rounds && access(budget, F_OK) != 0; round++) {
        pid_t pid = fork();
        if (pid == 0) {
            pthread_t t;
            if (pthread_create(&t, NULL, reuse_thread, NULL)) _exit(95);
            forever();
        }
        if (pid < 0 || waitpid(pid, NULL, 0) != pid) _exit(92);
    }
    shared->stop = 1;
    char text[64];
    snprintf(text, sizeof(text), "hits=%d", shared->hits);
    usleep(50000);
    write_pid(".hits", shared->hits);
    write_pid(".rounds", round);
    mark_text(text);
    for (;;) {
        char path[4096], data[32];
        path_for(path, sizeof(path), ".beat");
        int fd = open(path, O_WRONLY|O_CREAT|O_TRUNC, 0600);
        int len = snprintf(data, sizeof(data), "%lu", shared->beat);
        if (fd < 0 || write(fd, data, len) != len) _exit(96);
        close(fd);
        usleep(1000);
    }
}
// Starts a program with everything a process inherits set away from the defaults, as a parent that
// isn't this harness might leave it: every signal blocked (glibc's sigfillset, as in a C launcher),
// some ignored, and other values for nice, limits, oom_score_adj.
static int inherit(char **argv) {
    sigset_t all;
    sigfillset(&all);
    signal(SIGPIPE, SIG_IGN);
    signal(SIGHUP, SIG_IGN);
    signal(SIGINT, SIG_IGN);
    signal(SIGTERM, SIG_IGN);
    signal(SIGUSR1, SIG_IGN);
    signal(SIGRTMIN+3, SIG_IGN);
    if (setpriority(PRIO_PROCESS, 0, 5)) return 90;
    struct rlimit limit = {4096, 8192};
    if (setrlimit(RLIMIT_NOFILE, &limit)) return 90;
    limit.rlim_cur = limit.rlim_max = 1024*1024*1024;
    if (setrlimit(RLIMIT_FSIZE, &limit)) return 90;
    limit.rlim_cur = limit.rlim_max = 0;
    if (setrlimit(RLIMIT_CORE, &limit)) return 90;
    int fd = open("/proc/self/oom_score_adj", O_WRONLY);
    if (fd < 0 || write(fd, "500", 3) != 3) return 90;
    close(fd);
    if (sigprocmask(SIG_SETMASK, &all, NULL)) return 90;
    execv(argv[0], argv);
    return 91;
}
int main(int argc, char **argv) {
    if (argc > 3 && strcmp(argv[1], "inherit") == 0) return inherit(argv+2);
    if (argc != 3) return 93;
    executable = argv[0]; mode = argv[1]; marker = argv[2];
    if (strcmp(mode, "exit-now") == 0) _exit(0);
    if (strcmp(mode, "exec-target") == 0) { mark_text("exec-ready"); heartbeat(); }
    if (strcmp(mode, "pid-beat") == 0) { write_pid(".pid", getpid()); mark(); heartbeat(); }
    if (strcmp(mode, "kernel-beat") == 0) kernel_beat();
    if (strcmp(mode, "stuck-pause") == 0) stuck_pause();
    if (strcmp(mode, "chaos") == 0) chaos();
    if (strcmp(mode, "failing-execs") == 0) {
        pthread_t t;
        if (pthread_create(&t, NULL, failing_execs, NULL)) return 95;
        heartbeat();
    }
    if (strcmp(mode, "execveat-thread") == 0) {
        pthread_t t;
        if (pthread_create(&t, NULL, execveat_thread, NULL)) return 95;
        forever();
    }
    if (strncmp(mode, "sigcont-children", 16) == 0) sigcont_children();
    if (strcmp(mode, "self-stop") == 0) self_stop();
    if (strncmp(mode, "tid-reuse", 9) == 0) tid_reuse();
    if (strcmp(mode, "listener-exec") == 0) listener_tree();
    if (strcmp(mode, "stacked-exec") == 0) {
        pthread_t t;
        if (pthread_create(&t, NULL, stacked_exec, NULL)) return 95;
        forever();
    }
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
