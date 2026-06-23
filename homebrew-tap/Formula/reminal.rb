class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.4/reminal_0.7.4_darwin_arm64.tar.gz"
      sha256 "029e8f29aaf95952e2b9c020bcd933994ef29215b051f33f1839879bab55609c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.4/reminal_0.7.4_darwin_amd64.tar.gz"
      sha256 "e1742eee7adcb7b9ab06ba132789021b521393fd3496949c7e3f24990aad1bd0"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.4/reminal_0.7.4_linux_arm64.tar.gz"
      sha256 "ddc249726c3522f43b0b5ccd503953c875fd5fa51cd4e7672ed7ede00ef60fda"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
