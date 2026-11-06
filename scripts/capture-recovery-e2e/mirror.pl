# A session's side of the daemon's capture socket, for check.sh: asks for a
# window's stream, and when the daemon ends it, asks again half a second later
# (as a session does). Prints one line per event, with seconds since the
# start: "<t> frame", "<t> error <why>", "<t> ended", then a summary.
#   perl mirror.pl <socket> <window id> <seconds>
use strict;
use IO::Socket::UNIX;
use Time::HiRes qw(time sleep);
my ($sock, $win, $secs) = @ARGV;
my $t0 = time;
my ($frames, $first, $last, $gap, $ends, @errs) = (0, -1, -1, 0, 0);
sub ev { printf "%.1f %s\n", time - $t0, $_[0] }
while (time - $t0 < $secs) {
  my $c = IO::Socket::UNIX->new(Peer => $sock, Type => SOCK_STREAM) or do { sleep 0.5; next };
  print $c "capture $win 640 45 5\n";
  $c->blocking(1);
  while (time - $t0 < $secs) {
    my $rin = ''; vec($rin, fileno($c), 1) = 1;
    next unless select(my $rout = $rin, undef, undef, 0.5);
    my $hdr = ''; last unless read($c, $hdr, 4) == 4;
    my $n = unpack('N', $hdr);
    if ($n == 0xFFFFFFFF) {
      read($c, $hdr, 4); my $m = ''; read($c, $m, unpack('N', $hdr));
      push @errs, $m; ev("error $m"); next;
    }
    my $b = ''; last unless read($c, $b, $n) == $n;
    my $now = time - $t0;
    $gap = $now - $last if $last >= 0 && $now - $last > $gap;
    $first = $now if $first < 0;
    $last = $now; $frames++;
  }
  close $c; $ends++; ev("ended");
  sleep 0.5;
}
printf "summary frames=%d first=%.1f maxgap=%.1f ends=%d errors=%d\n", $frames, $first, $gap, $ends, scalar @errs;
