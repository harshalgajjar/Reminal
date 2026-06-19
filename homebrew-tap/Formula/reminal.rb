class Reminal < Formula
  desc "Remote terminal access from any browser — no SSH, no port forwarding"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.1.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.0/reminal_0.1.0_darwin_arm64.tar.gz"
      sha256 "854a6fb8a2b0e82277a592a93062c0d0e6528eed60f03e8d90213a319dc88e37"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.0/reminal_0.1.0_darwin_amd64.tar.gz"
      sha256 "cab8d6d93d92d21e0cfb144173f1ff7155c12704f41b61154a0c2d26488e84ce"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.0/reminal_0.1.0_linux_arm64.tar.gz"
      sha256 "2440fa12dc97fca984c029a43379da84dc642364ab9d293120b240eb8fc68145"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
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
