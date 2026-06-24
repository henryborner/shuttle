#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <time.h>
#include <immintrin.h>

typedef int32_t int32;
typedef uint32_t uint32;
typedef int8_t schar;

// Pure C reference (from rsync checksum.c, CHAR_OFFSET=0)
static uint32 get_checksum1_c(char *buf1, int32 len) {
    int32 i;
    uint32 s1 = 0, s2 = 0;
    schar *buf = (schar *)buf1;
    for (i = 0; i < (len-4); i+=4) {
        s2 += 4*(s1 + buf[i]) + 3*buf[i+1] + 2*buf[i+2] + buf[i+3];
        s1 += (buf[i+0] + buf[i+1] + buf[i+2] + buf[i+3]);
    }
    for (; i < len; i++) { s1 += buf[i]; s2 += s1; }
    return (s1 & 0xffff) + (s2 << 16);
}

#define ROUNDS 1024
#define BLOCK_LEN (1024*1024)

int main() {
    unsigned char* buf = (unsigned char*)aligned_alloc(64, BLOCK_LEN);
    for (int i = 0; i < BLOCK_LEN; i++) buf[i] = (i + (i % 3) + (i % 11)) % 256;

    struct timespec start, end;
    uint32 cs;
    clock_gettime(CLOCK_MONOTONIC_RAW, &start);
    for (int r = 0; r < ROUNDS; r++) {
        cs = get_checksum1_c((char*)buf, BLOCK_LEN);
    }
    clock_gettime(CLOCK_MONOTONIC_RAW, &end);
    uint64_t us = (end.tv_sec - start.tv_sec) * 1000000 + (end.tv_nsec - start.tv_nsec) / 1000;
    double mbps = (double)(BLOCK_LEN / (1024*1024)) * ROUNDS / ((double)us / 1000000.0);
    printf("rsync-C  :: %5.0f MB/s :: %08x\n", mbps, cs);

    free(buf);
    return 0;
}
