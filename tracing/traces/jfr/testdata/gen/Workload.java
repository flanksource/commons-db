package jfrfixture;

import java.util.concurrent.locks.LockSupport;

// Workload is the program jdk-profile.jfr.gz was recorded from: one thread
// burning CPU, one allocating, two contending on a monitor and one parking, so
// a JDK Flight Recording with settings=profile holds every event the jfr trace
// kind decodes. See ../README.md for the command that records it.
public class Workload {
    private static final Object LOCK = new Object();
    private static volatile long sink;
    private static volatile boolean running = true;

    public static void main(String[] args) throws Exception {
        long millis = args.length > 0 ? Long.parseLong(args[0]) : 2000;
        Thread[] threads = {
            new Thread(Workload::burn, "burner"),
            new Thread(Workload::allocate, "allocator"),
            new Thread(Workload::contend, "contender-1"),
            new Thread(Workload::contend, "contender-2"),
            new Thread(Workload::park, "parker"),
        };
        for (Thread thread : threads) {
            thread.start();
        }
        Thread.sleep(millis);
        running = false;
        for (Thread thread : threads) {
            thread.join();
        }
    }

    static void burn() {
        long acc = 0;
        while (running) {
            for (int i = 0; i < 1_000_000; i++) {
                acc = acc * 31 + i;
            }
            sink = acc;
        }
    }

    static void allocate() {
        while (running) {
            byte[][] chunks = new byte[64][];
            for (int i = 0; i < chunks.length; i++) {
                chunks[i] = new byte[16 * 1024];
            }
            sink = chunks[chunks.length - 1].length;
        }
    }

    static void contend() {
        while (running) {
            synchronized (LOCK) {
                hold(30);
            }
            hold(1);
        }
    }

    static void hold(long millis) {
        long until = System.nanoTime() + millis * 1_000_000;
        long acc = 0;
        while (System.nanoTime() < until) {
            acc++;
        }
        sink = acc;
    }

    static void park() {
        while (running) {
            LockSupport.parkNanos(Workload.class, 25_000_000);
        }
    }
}
