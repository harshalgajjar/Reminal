# Reads `reminal-capture serve` output on stdin and prints how many frames came
# for sid 1, then any error it sent: "frames=<n> error=<text>".
use strict;
binmode STDIN;
my ($frames, $err) = (0, '');
while (read(STDIN, my $hdr, 8) == 8) {
  my ($sid, $len) = unpack('NN', $hdr);
  my $p = '';
  read(STDIN, $p, $len) == $len or last if $len;
  next unless $sid == 1 && $len;
  my ($n) = unpack('N', $p);
  if ($n == 0xFFFFFFFF) { $err = substr($p, 8) } else { $frames++ }
}
print "frames=$frames error=$err\n";
