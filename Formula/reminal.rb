class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.6.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.0/reminal_1.6.0_darwin_arm64.tar.gz"
      sha256 "f2f651aafb865a8dda1efde282befbff62043528c19e296541f50fdb5d947d04"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.0/reminal_1.6.0_darwin_amd64.tar.gz"
      sha256 "672076e9d3ea98a7e2be9eb1bb8bd8c33a46288a897d902072570eb175cc2be7"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.0/reminal_1.6.0_linux_arm64.tar.gz"
      sha256 "f0178c09aff8398dae3cc6e27245036ac29956a57d3320829a454524460e4bdb"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.0/reminal_1.6.0_linux_amd64.tar.gz"
      sha256 "29847b3809f8941b3d031bb4cbe4504801aa4a7d9f387ebb371052b6693fae11"
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
